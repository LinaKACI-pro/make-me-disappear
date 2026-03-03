package imap

import "regexp"

type replyKind int

const (
	replyUnknown   replyKind = iota
	replyNoData              // broker says they have no data on the user
	replyConfirmed           // broker confirms deletion/opt-out
)

type classification struct {
	kind  replyKind
	score int // number of matching patterns
}

var noDataPatterns = []*regexp.Regexp{
	// English
	regexp.MustCompile(`(?i)no\s+record`),
	regexp.MustCompile(`(?i)no\s+data\s+(found|on\s+(file|you|record))`),
	regexp.MustCompile(`(?i)(unable|couldn.t|could not)\s+(to\s+)?locate`),
	regexp.MustCompile(`(?i)(unable|couldn.t|could not)\s+(to\s+)?find\s+(any|your)`),
	regexp.MustCompile(`(?i)(we\s+)?(do(es)?\s+not|don.t|doesn.t|cannot|can.t)\s+(have|find|locate|hold)\s+(any|your)`),
	regexp.MustCompile(`(?i)not\s+(found in|present in)\s+(our|the)\s+(database|system|records)`),
	regexp.MustCompile(`(?i)no\s+personal\s+(data|information)\s+(on file|found|in our)`),
	regexp.MustCompile(`(?i)we\s+have\s+no\s+(information|data|record)`),
	regexp.MustCompile(`(?i)found\s+no\s+(data|record|information|match)`),
	regexp.MustCompile(`(?i)no\s+information\s+(was\s+)?(found|located|associated)`),
	// French
	regexp.MustCompile(`(?i)aucune?\s+donn[eé]e`),
	regexp.MustCompile(`(?i)aucun\s+enregistrement`),
	regexp.MustCompile(`(?i)aucune?\s+information`),
	regexp.MustCompile(`(?i)(ne\s+d[eé]tenons\s+pas|n.avons\s+pas)\s+(de\s+donn[eé]es|d.informations|de\s+fichier)`),
	regexp.MustCompile(`(?i)ne\s+sont\s+pas\s+(enregistr[eé]es|r[eé]f[eé]renci?[eé]es)`),
	regexp.MustCompile(`(?i)ne\s+figure(nt|z)?\s+pas\s+dans\s+(notre|nos)`),
	regexp.MustCompile(`(?i)introuvable`),
	// German
	regexp.MustCompile(`(?i)keine\s+(Daten|Informationen|Eintr[äa]ge?)`),
	regexp.MustCompile(`(?i)nicht\s+(gefunden|vorhanden|gespeichert)`),
	// Spanish
	regexp.MustCompile(`(?i)no\s+(tenemos|hemos\s+encontrado)\s+(datos|informaci[oó]n)`),
	// Dutch
	regexp.MustCompile(`(?i)geen\s+(gegevens|informatie|records)`),
}

var confirmedPatterns = []*regexp.Regexp{
	// English
	regexp.MustCompile(`(?i)(have\s+been|was)\s+(deleted|removed|erased)\s+(from\s+our|successfully)`),
	regexp.MustCompile(`(?i)your\s+(data|information|record).{0,30}(deleted|removed|erased|processed)`),
	regexp.MustCompile(`(?i)(request|deletion|opt.?out)\s+(has\s+been|was)\s+(processed|completed|honored|fulfilled)`),
	regexp.MustCompile(`(?i)successfully\s+(processed|removed|deleted|opted.?out|unsubscribed)`),
	// French
	regexp.MustCompile(`(?i)votre\s+compte\s+a\s+[eé]t[eé]\s+(compl[eéè]tement?|enti[eèé]rement?)\s+supprim[eé]`),
	regexp.MustCompile(`(?i)(supprim[eé]|effac[eé]|trait[eé]e?)\s+(de\s+(notre|nos)|avec\s+succ[eè]s)`),
	regexp.MustCompile(`(?i)votre\s+demande\s+(a\s+[eé]t[eé]|est)\s+(trait[eé]e?|prise\s+en\s+compte|honor[eé]e?)`),
	regexp.MustCompile(`(?i)avons\s+(supprim[eé]|effac[eé]|retir[eé])\s+(vos|votre)`),
}

// classifyReply analyses a raw email body and returns a classification.
// A score >= 1 on a specific pattern list indicates that kind.
func classifyReply(body string) classification {
	var noDataScore, confirmedScore int
	for _, pat := range noDataPatterns {
		if pat.MatchString(body) {
			noDataScore++
		}
	}
	for _, pat := range confirmedPatterns {
		if pat.MatchString(body) {
			confirmedScore++
		}
	}
	switch {
	case noDataScore >= 1:
		return classification{replyNoData, noDataScore}
	case confirmedScore >= 1:
		return classification{replyConfirmed, confirmedScore}
	default:
		return classification{replyUnknown, 0}
	}
}
