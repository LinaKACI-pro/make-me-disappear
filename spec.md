# Spec : Personal Data Removal Bot (auto-hébergé)

**Version 0.2 — révisée**  
**Stack cible : Go + Playwright (Node) + SQLite**

---

## 1. Problème racine

Des centaines d'entreprises appelées **data brokers** collectent, agrègent et revendent des données personnelles (nom, adresse, email, téléphone, profil socio-démographique, historique de navigation) sans consentement explicite. En Europe, le RGPD (Article 17 — droit à l'effacement) oblige ces entités à supprimer les données sur demande dans un délai de 30 jours. Aux États-Unis, le CCPA donne un droit similaire aux résidents californiens.

Le problème : il y a ~300–500 brokers actifs. Faire les demandes manuellement prend des dizaines d'heures. Et les données réapparaissent régulièrement car les brokers rachètent des bases entre eux.

**Ce système résout ça en automatisant intégralement le cycle demande → suivi → relance.**

---

## 2. Périmètre & objectifs

### In scope
- Envoi automatique de demandes de suppression (email + formulaires web)
- Suivi des statuts et relances automatiques
- Cycle récurrent (les données réapparaissent, il faut relancer tous les ~3 mois)
- Couvrir les brokers EU en priorité (RGPD = levier légal fort), puis US

### Out of scope (v1)
- Interface graphique poussée (CLI + dashboard HTML basique suffisent)
- Multi-utilisateur (usage personnel uniquement)
- Scan actif pour découvrir si tes données sont présentes (on envoie à tous les brokers connus, peu importe)

---

## 3. Modèle de données

### `brokers`
```
id           TEXT PRIMARY KEY   -- slug ex: "spokeo", "whitepages"
name         TEXT
region       TEXT               -- "EU" | "US" | "GLOBAL"
method       TEXT               -- "email" | "form" | "manual"
contact      TEXT               -- email ou URL du formulaire
opt_out_url  TEXT               -- URL directe si form
notes        TEXT               -- ex: "nécessite scan ID"
active       BOOL DEFAULT true
last_updated DATETIME
```

### `requests`

> **[CHANGEMENT v0.2]** Le champ `cycle` est supprimé. Chaque cycle crée de nouvelles rows. L'historique se reconstitue naturellement via `GROUP BY broker_id ORDER BY sent_at DESC`. Ça élimine la logique d'incrémentation de cycle.

```
id           INTEGER PRIMARY KEY AUTOINCREMENT
broker_id    TEXT REFERENCES brokers(id)
status       TEXT     -- "pending" | "sent" | "needs_review" | "confirmed" | "rejected" | "manual_required" | "error"
method_used  TEXT     -- "email" | "form"
sent_at      DATETIME
last_action  DATETIME
next_retry   DATETIME
attempt      INTEGER DEFAULT 1
response_raw TEXT     -- réponse brute (email ou HTTP response)
notes        TEXT
```

### `identity`  (config locale, pas en DB)

> **[CHANGEMENT v0.2]** Les secrets (mots de passe SMTP/IMAP) sont lus depuis des variables d'environnement. Le fichier config.yaml ne contient plus de secrets en clair et doit avoir les permissions `600`.

```yaml
# config.yaml — chmod 600
identity:
  first_name: "Lili"
  last_name:  "..."
  email:      "..."           # email dédié recommandé (voir section 7)
  phone:      "..."           # optionnel
  address:    "..."           # optionnel
  country:    "FR"

smtp:
  host:     "smtp.gmail.com"
  port:     587
  user:     "..."
  password: "${SMTP_PASSWORD}"   # lu depuis env var SMTP_PASSWORD

imap:
  host:     "imap.gmail.com"
  port:     993
  user:     "..."
  password: "${IMAP_PASSWORD}"   # lu depuis env var IMAP_PASSWORD
```

Exemple de lancement :
```bash
export SMTP_PASSWORD="xxxx"
export IMAP_PASSWORD="xxxx"
./bot run
```

En Go, la lecture est triviale :
```go
func resolveEnv(val string) string {
    if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
        return os.Getenv(val[2 : len(val)-1])
    }
    return val
}
```

---

## 4. Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Scheduler (Go)                     │
│   Cron ticker — lance les workers à intervalles     │
│   Concurrence : 1 job à la fois, délai aléatoire   │
│   entre chaque (2-10s) pour éviter la détection     │
└──────────────┬──────────────────┬───────────────────┘
               │                  │
     ┌─────────▼──────┐  ┌────────▼────────┐
     │  Email Worker  │  │   Form Worker   │
     │  (Go net/smtp) │  │  (Playwright JS)│
     │                │  │  Long-running   │
     │                │  │  process stdin  │
     └─────────┬──────┘  └────────┬────────┘
               │                  │
               └────────┬─────────┘
                        │
              ┌─────────▼──────────┐
              │   SQLite DB        │
              │  (requests log)    │
              └─────────┬──────────┘
                        │
              ┌─────────▼──────────┐
              │  IMAP Watcher (Go) │
              │  log & match only  │
              │  → status          │
              │    "needs_review"  │
              └────────────────────┘
```

### Composants

**1. Scheduler**  
Tourne en background (daemon ou cron système). Lit la DB, trouve les requests `pending` ou dont `next_retry` est dépassé, dispatche vers Email Worker ou Form Worker selon `method`.

**Concurrence : séquentielle, 1 job à la fois.** Délai aléatoire entre chaque envoi (2–10 secondes). Raisons :
- Évite le rate limiting / détection anti-bot
- Réduit la consommation RAM (pas de spawns parallèles)
- On n'est pas pressé : les brokers ont 30 jours pour répondre

**2. Email Worker**  
- Génère le template RGPD personnalisé avec les infos identité
- Envoie via SMTP
- Log dans DB avec status `sent`

**3. Form Worker**

> **[CHANGEMENT v0.2]** Le Form Worker est un process Node.js **long-running** au lieu d'un spawn par broker. Il écoute sur stdin (JSON lines) et maintient une seule instance Chromium.

- Process Node.js démarré une seule fois par le scheduler Go
- Communication via stdin/stdout (JSON lines, un job par ligne)
- Maintient **une seule instance Chromium** → mémoire stable (~200-400 MB au lieu de N × 400 MB)
- Le scheduler envoie un job JSON : `{"broker_id": "spokeo", "url": "...", "identity": {...}}`
- Le worker répond sur stdout : `{"success": true, "screenshot_path": "...", "error": null}`
- Si le process crash → le scheduler le relance automatiquement

Protocole simplifié :
```
[Go scheduler] --stdin→ {"broker_id":"spokeo","url":"...","identity":{...}}
[Node worker]  --stdout→ {"success":true,"screenshot_path":"/tmp/spokeo.png"}
```

**4. IMAP Watcher**

> **[CHANGEMENT v0.2]** Le watcher ne tente plus de classifier automatiquement les emails. Il détecte, associe par domaine expéditeur (best-effort), et flag `needs_review`.

- Tourne en boucle, poll toutes les 15 min
- Parse les emails entrants : extrait l'expéditeur et le body
- Association au broker : match sur le domaine de l'expéditeur (ex: `noreply@spokeo.com` → broker `spokeo`). Si aucun match → log en `unmatched` pour review manuelle
- **Pas de classification automatique** (confirmed / rejected) — trop fragile vu l'hétérogénéité des réponses (templates auto, réponses humaines, multilingue, PDFs)
- Tout email matché est stocké avec status `needs_review` + le body dans `response_raw`
- Review manuelle via CLI : `bot inbox:review` (affiche les emails non classifiés un par un, tu choisis le statut)

Pourquoi pas de classification auto en v1 :
- Les brokers répondent depuis des adresses différentes du contact (noreply@, support@, domaine subsidiaire)
- Les formats sont extrêmement variés (HTML, texte, PDF attaché)
- Le temps de debug du parser > le temps de review manuelle (~2 min/semaine)

**5. Retry Engine**  
Logique dans le scheduler :
- Si status `sent` depuis > 35 jours et pas de réponse → relance (attempt++)
- Si attempt > 3 → passe en `manual_required`
- Si un cycle est terminé (tous les brokers ont un statut final) → créer de nouvelles rows `pending` pour le prochain cycle (dans 90 jours)

---

## 5. Templates RGPD

> **[CHANGEMENT v0.2]** Ajout d'une ligne optionnelle mentionnant le profil public du broker quand l'URL existe, pour rendre la demande plus difficile à ignorer.

Template email de base (paramétrisé) :

```
Subject: Demande d'effacement de données personnelles — Article 17 RGPD

Madame, Monsieur,

Conformément au Règlement (UE) 2016/679 (RGPD), Article 17,
je vous adresse une demande d'effacement de l'ensemble des
données personnelles me concernant que vous détenez.

{{if .OptOutURL}}
J'ai constaté que votre site affiche des résultats me
concernant à l'adresse suivante : {{.OptOutURL}}
{{end}}

Mes informations :
  Nom complet : {{.FullName}}
  Email       : {{.Email}}
  {{if .Phone}}Téléphone   : {{.Phone}}{{end}}
  {{if .Address}}Adresse     : {{.Address}}{{end}}

Je vous demande de :
1. Supprimer l'intégralité de mes données personnelles
2. Confirmer la suppression par retour d'email
3. Cesser tout traitement futur de mes données

Vous disposez de 30 jours pour répondre à cette demande
(Article 12(3) RGPD). En cas de non-réponse, je me réserve
le droit de saisir la CNIL.

Cordialement,
{{.FullName}}
```

Variante anglaise pour brokers US/GLOBAL :

```
Subject: Data Deletion Request — GDPR Article 17 / CCPA

Dear Data Protection Officer,

Pursuant to GDPR Article 17 and/or the California Consumer
Privacy Act (CCPA), I hereby request the permanent deletion
of all personal data you hold about me.

{{if .OptOutURL}}
I have found that your website displays personal information
about me at the following URL: {{.OptOutURL}}
{{end}}

My identifying information:
  Full name : {{.FullName}}
  Email     : {{.Email}}
  {{if .Phone}}Phone     : {{.Phone}}{{end}}
  {{if .Address}}Address   : {{.Address}}{{end}}

I request that you:
1. Delete all personal data you hold about me
2. Confirm the deletion by return email
3. Cease any future processing of my data

You have 30 days to respond to this request (GDPR Article
12(3)). Failure to comply may result in a complaint to the
relevant supervisory authority.

Sincerely,
{{.FullName}}
```

---

## 6. Broker list — source de vérité

Ne pas recréer la liste from scratch. Partir de :

**`https://github.com/yaelwrites/Big-Ass-Data-Broker-Opt-Out-List`**

Ce repo liste ~200 brokers avec méthode (email / form / manual), URL, et notes. On le parse et on importe dans la DB au démarrage (`db:seed`).

### Stratégie de couverture par tiers

> **[CHANGEMENT v0.2]** On ne scripte pas un formulaire Playwright par broker. On utilise une approche par tiers.

- **Tier 1 — Scripts Playwright custom** : les 10-15 brokers US les plus importants (Spokeo, WhitePages, Intelius, BeenVerified, PeopleFinder, MyLife, etc.). Ceux-ci ont des formulaires bien définis et valent l'investissement d'un script dédié.
- **Tier 2 — Email au DPO** : tous les autres brokers, y compris ceux dont la méthode officielle est un formulaire. **Le RGPD n'impose pas de passer par le formulaire du broker.** Un email envoyé au DPO (ou à l'adresse privacy@) est juridiquement valide et suffisant. On override le champ `method` à `email` pour ces brokers au seed.
- **Tier 3 — Manual** : brokers qui requièrent un scan d'identité ou un process non automatisable. Flaggés `manual_required` dès le seed.

Cette approche ramène ~80% des brokers dans le flow email (déjà automatisé) et limite les scripts Playwright à une dizaine.

Format d'import YAML :

```yaml
brokers:
  # Tier 1 — script Playwright dédié
  - id: "spokeo"
    name: "Spokeo"
    region: "US"
    method: "form"
    opt_out_url: "https://www.spokeo.com/optout"
    tier: 1
    notes: ""

  - id: "whitepages"
    name: "WhitePages"
    region: "US"
    method: "form"
    opt_out_url: "https://www.whitepages.com/suppression-requests"
    tier: 1

  # Tier 2 — email au DPO (même si le broker propose un form)
  - id: "datacoup"
    name: "Datacoup"
    region: "EU"
    method: "email"
    contact: "privacy@datacoup.com"
    tier: 2

  - id: "acxiom"
    name: "Acxiom"
    region: "GLOBAL"
    method: "email"
    contact: "privacy@acxiom.com"
    opt_out_url: "https://isapps.acxiom.com/optout/optout.aspx"
    tier: 2
    notes: "Form existe mais email au DPO suffit légalement"

  # Tier 3 — manual
  - id: "lexisnexis"
    name: "LexisNexis"
    region: "US"
    method: "manual"
    contact: "privacy@lexisnexis.com"
    tier: 3
    notes: "Requiert scan ID + formulaire spécifique"
```

---

## 7. Considérations pratiques importantes

### Email dédié
Créer une adresse email spécifique pour ce projet (ex: `lili.optout@gmail.com`).
Raisons :
- Éviter le spam dans ta boîte principale
- Faciliter le parsing IMAP (tout ce qui rentre est lié aux opt-outs)
- Certains brokers mal intentionnés peuvent re-vendre l'email utilisé

### Gestion des CAPTCHAs
- La majorité des formulaires simples passent avec Playwright + délais humains simulés
- Pour les CAPTCHAs reCAPTCHA v2 : intégrer `2captcha` API (~$3/1000)
- Pour hCaptcha : même chose
- Si CAPTCHA non résolu → status `manual_required`, notification

### Cas "scan ID requis"
Environ 10–15% des brokers US demandent une pièce d'identité. Non automatisable en v1 → flaggé `manual_required` dès le seed (Tier 3).

### Faux positifs de confirmation
Certains brokers envoient un email de confirmation auto mais ne suppriment rien. Le système log la réponse mais le vrai indicateur reste le cycle suivant (si les données réapparaissent, le broker est unreliable → noter dans `brokers.notes`).

---

## 8. CLI — interface principale

```bash
# Seeder la DB avec les brokers connus
./bot db:seed

# Créer les requests initiales pour tous les brokers actifs
./bot campaign:init

# Lancer le scheduler en mode daemon
./bot run

# Voir le statut global
./bot status

# Lancer manuellement pour un broker spécifique
./bot send --broker spokeo

# Voir les cas manuels à traiter
./bot manual:list

# Review des emails entrants non classifiés
./bot inbox:review

# Dashboard HTML (ouvre dans le browser)
./bot dashboard
```

---

## 9. Dashboard (HTML statique généré)

> **[CHANGEMENT v0.2]** Généré directement via `html/template` en Go. Pas de serveur web, pas de framework. Le CLI écrit un fichier HTML et l'ouvre.

```bash
./bot dashboard
# → génère /tmp/bot-dashboard.html et ouvre dans le browser
```

Page HTML simple générée depuis la DB :

```
┌─────────────────────────────────────────────────────┐
│  Data Removal Bot — Dernier cycle                   │
│  Dernière mise à jour : 2025-02-26 18:42            │
├─────────────┬─────────┬───────────┬─────────────────┤
│ Broker      │ Région  │ Statut    │ Dernier contact │
├─────────────┼─────────┼───────────┼─────────────────┤
│ Spokeo      │ US      │ ✅ Confirm│ 2025-02-10      │
│ WhitePages  │ US      │ 🕐 Sent   │ 2025-02-26      │
│ Intelius    │ US      │ ⚠️ Manual │ —               │
│ Datacoup    │ EU      │ ✅ Confirm│ 2025-02-15      │
│ Acxiom      │ GLOBAL  │ 🔍 Review │ 2025-02-20      │
│ ...         │ ...     │ ...       │ ...             │
└─────────────┴─────────┴───────────┴─────────────────┘

Résumé : 47 confirmés | 102 en attente | 5 à review | 12 manuels | 8 erreurs
```

Implémentation Go :
```go
func generateDashboard(db *sql.DB) error {
    tmpl := template.Must(template.ParseFiles("templates/dashboard.html"))
    data := fetchDashboardData(db)
    f, _ := os.Create("/tmp/bot-dashboard.html")
    defer f.Close()
    tmpl.Execute(f, data)
    exec.Command("xdg-open", "/tmp/bot-dashboard.html").Start()
    return nil
}
```

---

## 10. Plan d'implémentation (phases)

### Phase 1 — Core email (2–3 jours)
- Setup projet Go + SQLite
- `db:seed` avec YAML brokers (Tier 1/2/3)
- Config loader avec support env vars pour secrets
- Email Worker (SMTP) avec templates FR + EN
- IMAP Watcher (mode log-only → `needs_review`)
- Retry logic basique
- CLI : `run`, `status`, `inbox:review`

→ À ce stade : couvre ~80% des brokers via email (Tier 2 inclus)

### Phase 2 — Form automation (2–3 jours)
- Setup Playwright Node.js — process long-running (stdin/stdout JSON lines)
- Scripts pour les 10 brokers Tier 1 (Spokeo, WhitePages, Intelius, BeenVerified, PeopleFinder, MyLife, etc.)
- Intégration 2captcha (optionnel)

→ Monte à ~90% de couverture automatisée

### Phase 3 — Polish (1 jour)
- Dashboard HTML via `html/template`
- Notifications (email à soi-même pour les cas `needs_review` et `manual_required`)
- Logging structuré
- Cycle récurrent : au démarrage du scheduler, check si le dernier cycle date de > 90 jours → créer les nouvelles rows `pending`

---

## 11. Risques & mitigations

| Risque | Impact | Mitigation |
|--------|--------|------------|
| Formulaires qui changent | Moyen | Limité à 10 scripts Tier 1, facile à maintenir |
| Brokers qui ignorent | Faible | Retry × 3 puis manual, log pour référence |
| Email dédié blacklisté | Faible | Utiliser un alias ou changer d'adresse, data en DB reste valide |
| Captchas insolubles | Moyen | 2captcha API ou fallback manual |
| Broker demande ID scan | Bloquant | Flaggé Tier 3 / manual dès le seed |
| Broker recontacte / spam | Faible | Email dédié isole la boîte principale |
| Classification email incorrecte | Éliminé | Pas de classification auto — review manuelle |
| RAM Chromium | Éliminé | Process long-running avec instance unique |

---

## 12. Non-objectifs explicites

- Pas de GUI lourde (waste of time pour usage solo)
- Pas de containerisation Docker en v1 (binary Go + script Node, c'est suffisant)
- Pas de cloud hosting (auto-hébergé sur ton homelab, tourne sur n'importe quelle machine)
- Pas de feature "scan si tes données sont présentes" — on envoie à tout le monde de toute façon, c'est gratuit légalement
- Pas de classification automatique des réponses email en v1
- Pas de scripts Playwright pour chaque broker — email au DPO par défaut

---

## Résumé des changements v0.1 → v0.2

| # | Point | Changement |
|---|-------|------------|
| 1 | Form Worker | Process long-running (stdin/stdout) au lieu de `exec.Command` par broker. Une seule instance Chromium. |
| 2 | IMAP Watcher | Mode log-only + `needs_review`. Pas de classification auto. Nouvelle commande `bot inbox:review`. |
| 3 | Couverture brokers | Stratégie par tiers : 10 scripts Playwright (Tier 1), email au DPO pour tout le reste (Tier 2), manual (Tier 3). |
| 4 | Template RGPD | Ajout ligne optionnelle mentionnant l'URL du profil public quand disponible. |
| 5 | Secret management | Passwords SMTP/IMAP lus depuis env vars, plus de secrets en clair dans config.yaml. |
| 6 | Modèle de données | Suppression du champ `cycle`. Chaque cycle = nouvelles rows. Historique via `GROUP BY + ORDER BY`. |

---

*Prochaine étape : Phase 1 — scaffolding Go + db:seed + email worker.*
