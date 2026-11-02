package checkout

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type AddressGeographyEntry struct {
	ID             string  `json:"id"`
	Country        string  `json:"country"`
	Kind           string  `json:"kind"`
	Code           string  `json:"code"`
	ParentID       *string `json:"parent_id"`
	NameEN         string  `json:"name_en"`
	NameTA         string  `json:"name_ta"`
	MasterRevision int64   `json:"master_revision"`
}
type AddressGeography struct {
	AreaID string                  `json:"area_id"`
	Path   []AddressGeographyEntry `json:"path"`
}

func validAddressText(value string, min, max int) bool {
	value = strings.TrimSpace(value)
	count := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && count >= min && count <= max && !strings.ContainsFunc(value, unicode.IsControl)
}

func geographyJSON(value *AddressGeography) []byte {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	return raw
}

// Normal HTTP startup uses this constructor. The original constructor supports
// isolated domain tests and in-process orchestration without an HTTP session.
func NewAuthenticatedPostgresStore(pool *pgxpool.Pool, clock func() time.Time) (*PostgresStore, error) {
	store, err := NewPostgresStore(pool, clock)
	if err != nil {
		return nil, err
	}
	store.addressSessions = true
	return store, nil
}
func (store *PostgresStore) lockAddressSession(ctx context.Context, tx pgx.Tx, scope Scope) error {
	if !store.addressSessions {
		return nil
	}
	if scope.SourceSessionID == "" {
		return ErrAddressSession
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT identity.lock_address_session($1,$2,$3,$4)`, scope.TenantID, scope.Country, scope.CustomerID, scope.SourceSessionID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrAddressSession
	}
	return nil
}

// Saving an address must work before commercial pricing/wallet publication.
// No published postal zone means "outside service area", never implied cover.
func (store *PostgresStore) AddressServiceability(scope Scope, postal, locality string) (bool, error) {
	if !postgresCheckoutScope(scope) {
		return false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	var serviceable bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commerce.postal_zones WHERE tenant_id=$1 AND country=$2 AND upper(postal_code)=upper(trim($3)) AND lower(locality)=lower(trim($4)) AND active)`, scope.TenantID, scope.Country, postal, locality).Scan(&serviceable)
	return serviceable, err
}
func resolveAddressGeography(ctx context.Context, tx pgx.Tx, scope Scope, value *Address, current *AddressGeography) error {
	value.Geography = current
	if value.requestedGeographyID == "" || current != nil && current.AreaID == value.requestedGeographyID {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT admin.lock_address_geography($1,$2,$3)`, scope.TenantID, scope.Country, value.requestedGeographyID).Scan(&raw); err != nil {
		return err
	}
	if raw == nil {
		return ErrInvalidRequest
	}
	return json.Unmarshal(raw, &value.Geography)
}
