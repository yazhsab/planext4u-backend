package checkout

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/payment"
)

// PostgresPayerResolver reads the authenticated customer's own verified
// contact profile. It never accepts payer identity from checkout request data.
type PostgresPayerResolver struct{ pool *pgxpool.Pool }

func NewPostgresPayerResolver(pool *pgxpool.Pool) (*PostgresPayerResolver, error) {
	if pool == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresPayerResolver{pool: pool}, nil
}

func (resolver *PostgresPayerResolver) Ready(ctx context.Context) error {
	var ready bool
	if err := resolver.pool.QueryRow(ctx, `
		SELECT to_regclass('identity.identities') IS NOT NULL
		   AND to_regclass('identity.profiles') IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM information_schema.columns
		       WHERE table_schema='identity' AND table_name='profiles' AND column_name='email'
		   )`).Scan(&ready); err != nil {
		return fmt.Errorf("check payer profile readiness: %w", err)
	}
	if !ready {
		return errors.New("payer profile is unavailable")
	}
	return nil
}

func (resolver *PostgresPayerResolver) ResolvePayer(parent context.Context, scope Scope) (payment.Payer, error) {
	if parent == nil || !postgresCheckoutScope(scope) {
		return payment.Payer{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(parent, checkoutConfigurationTimeout)
	defer cancel()
	var payer payment.Payer
	err := resolver.pool.QueryRow(ctx, `
		SELECT p.email,p.phone,p.display_name
		FROM identity.profiles p
		JOIN identity.identities i ON i.id=p.identity_id
		WHERE i.id=$1 AND i.tenant_id=$2 AND i.disabled_at IS NULL`, scope.CustomerID, scope.TenantID).Scan(&payer.Email, &payer.Phone, &payer.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Payer{}, ErrInvalidRequest
	}
	if err != nil {
		return payment.Payer{}, fmt.Errorf("load payer profile: %w", err)
	}
	payer.Email, payer.Phone, payer.Name = strings.ToLower(strings.TrimSpace(payer.Email)), strings.TrimSpace(payer.Phone), strings.TrimSpace(payer.Name)
	if (scope.Country == "NG" && !validPayerEmail(payer.Email)) || (payer.Email != "" && !validPayerEmail(payer.Email)) || !validPayerPhone(payer.Phone) {
		return payment.Payer{}, ErrPaymentMethod
	}
	return payer, nil
}

func validPayerEmail(value string) bool {
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value && strings.Contains(value, "@")
}

func validPayerPhone(value string) bool {
	if value == "" {
		return true
	}
	if len(value) < 8 || len(value) > 16 || value[0] != '+' {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
