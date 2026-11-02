package participantreview

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// LockParticipant checks the owning identity and holds its role and source
// session until commit. Replays require the same current authority as writes.
func LockParticipant(ctx context.Context, tx pgx.Tx, tenant, country, subject, session, role string) error {
	if !UUID(tenant) || !UUID(subject) || !UUID(session) || len(country) != 2 || role != "VENDOR" && role != "RIDER" {
		return ErrForbidden
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT identity.lock_participant_session($1,$2,$3,$4,$5)`, tenant, country, subject, session, role).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
