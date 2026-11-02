package fulfillment

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Call from READ COMMITTED transactions, before any replay or GPS readback.
// Lock order is policy then identity. The owning publishers take the exclusive
// counterpart; no granted decision is cached in the fulfillment service.
func authorizeDeliveryConsent(ctx context.Context, tx pgx.Tx, actor Actor, riderID string) error {
	for _, key := range []string{"delivery-policy:" + actor.TenantID + ":" + actor.Country, "consent:" + riderID} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, key); err != nil {
			return fmt.Errorf("lock delivery consent: %w", err)
		}
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT identity.delivery_consent_allowed($1::text,$2::char(2),$3::text,configuration.delivery_consent_policy($1::uuid,$2::char(2)))`, actor.TenantID, actor.Country, riderID).Scan(&allowed); err != nil {
		return fmt.Errorf("read delivery consent: %w", err)
	}
	if !allowed {
		return ErrDeliveryConsentRequired
	}
	return nil
}
