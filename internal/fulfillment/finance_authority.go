package fulfillment

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// The admin owner validates its current directory and source session under
// row locks held by the fulfillment transaction. A role header alone is never
// sufficient to read another account's money or acknowledge a finance action.
func lockFinanceOperator(ctx context.Context, tx pgx.Tx, actor Actor, capability string) error {
	if !actor.MFAVerified {
		return ErrMFARequired
	}
	if !fulfillmentIsUUID(actor.OperatorSessionID) || actor.OperatorRevision < 1 {
		return ErrForbidden
	}
	claimedRole := ""
	switch {
	case hasRole(actor, "FINANCE"):
		claimedRole = "FINANCE"
	case hasRole(actor, "SUPER_ADMIN"):
		claimedRole = "SUPER_ADMIN"
	case hasRole(actor, "OPS_ADMIN"):
		claimedRole = "OPS_ADMIN"
	default:
		return ErrForbidden
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT admin.lock_fulfillment_finance_operator($1,$2,$3,$4,$5,$6,$7)`, actor.TenantID, actor.Country, actor.Subject, actor.OperatorSessionID, actor.OperatorRevision, capability, claimedRole).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
