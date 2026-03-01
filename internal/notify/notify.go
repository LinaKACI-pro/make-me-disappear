package notify

import (
	"database/sql"
	"fmt"

	"github.com/LinaKACI-pro/make-me-disappear/internal/config"
	"github.com/LinaKACI-pro/make-me-disappear/internal/email"
)

// SendDigest sends a summary email if there are actionable items
// (needs_review or manual_required requests). Returns nil without
// sending if there is nothing to report.
func SendDigest(smtpCfg config.SMTP, to string, d *sql.DB) error {
	var reviewCount, manualCount int
	err := d.QueryRow(`SELECT COUNT(*) FROM requests WHERE status = 'needs_review'`).Scan(&reviewCount)
	if err != nil {
		return err
	}
	err = d.QueryRow(`SELECT COUNT(*) FROM requests WHERE status = 'manual_required'`).Scan(&manualCount)
	if err != nil {
		return err
	}

	if reviewCount == 0 && manualCount == 0 {
		return nil
	}

	subject := "Bot RGPD — actions requises"
	body := fmt.Sprintf("Bonjour,\n\nVoici le résumé de votre bot de suppression de données :\n\n"+
		"  - %d demande(s) à revoir (needs_review)\n"+
		"  - %d demande(s) nécessitant une action manuelle\n\n"+
		"Lancez 'bot inbox-review' pour traiter les emails en attente.\n"+
		"Lancez 'bot manual-list' pour voir les actions manuelles.\n\n"+
		"— Bot RGPD\n", reviewCount, manualCount)

	return email.Send(smtpCfg, to, to, subject, body)
}
