package checkout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const checkoutConfigurationTimeout = 5 * time.Second

// PostgresConfigurationProvider reads only active, published checkout rules.
// Administrative writes use the same tables, so changes become visible to
// subsequent quote and address operations without a process restart.
type PostgresConfigurationProvider struct{ pool *pgxpool.Pool }

func NewPostgresConfigurationProvider(pool *pgxpool.Pool) (*PostgresConfigurationProvider, error) {
	if pool == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresConfigurationProvider{pool: pool}, nil
}

func (provider *PostgresConfigurationProvider) Ready(ctx context.Context) error {
	var ready bool
	if err := provider.pool.QueryRow(ctx, `
		SELECT to_regclass('commerce.pricing_policies') IS NOT NULL
		   AND to_regclass('commerce.postal_zones') IS NOT NULL
		   AND to_regclass('commerce.delivery_slots') IS NOT NULL
		   AND to_regclass('commerce.promotions') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check checkout configuration readiness: %w", err)
	}
	if !ready {
		return errors.New("checkout configuration is unavailable")
	}
	return nil
}

func (provider *PostgresConfigurationProvider) Configuration(parent context.Context, scope Scope) (Configuration, error) {
	if parent == nil || !postgresCheckoutScope(scope) {
		return Configuration{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(parent, checkoutConfigurationTimeout)
	defer cancel()
	result := Configuration{}

	rows, err := provider.pool.Query(ctx, `
		SELECT version,product_tax_basis_points,product_tax_treatment,
		       platform_fee_minor,platform_fee_tax_basis_points,wallet_point_value_minor,
		       wallet_mode,quote_ttl_seconds,reservation_ttl_seconds
		FROM commerce.pricing_policies
		WHERE tenant_id=$1 AND country=$2 AND active
		ORDER BY published_at DESC`, scope.TenantID, scope.Country)
	if err != nil {
		return Configuration{}, fmt.Errorf("load checkout pricing policy: %w", err)
	}
	for rows.Next() {
		var value PricingPolicy
		var quoteSeconds, reservationSeconds int64
		if err := rows.Scan(&value.Version, &value.ProductTaxBasisPoints, &value.ProductTaxTreatment,
			&value.PlatformFeeMinor, &value.PlatformFeeTaxBasisPoints, &value.WalletPointValueMinor,
			&value.WalletMode, &quoteSeconds, &reservationSeconds); err != nil {
			rows.Close()
			return Configuration{}, fmt.Errorf("scan checkout pricing policy: %w", err)
		}
		value.Country, value.QuoteTTL, value.ReservationTTL = scope.Country, time.Duration(quoteSeconds)*time.Second, time.Duration(reservationSeconds)*time.Second
		result.Policies = append(result.Policies, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Configuration{}, fmt.Errorf("iterate checkout pricing policy: %w", err)
	}
	rows.Close()

	rows, err = provider.pool.Query(ctx, `
		SELECT id::text,window_start,window_end,fee_minor,currency,capacity
		FROM commerce.delivery_slots
		WHERE tenant_id=$1 AND country=$2 AND capacity > 0
		ORDER BY window_start,id`, scope.TenantID, scope.Country)
	if err != nil {
		return Configuration{}, fmt.Errorf("load checkout delivery slots: %w", err)
	}
	for rows.Next() {
		var value DeliverySlot
		if err := rows.Scan(&value.ID, &value.WindowStart, &value.WindowEnd, &value.Fee.AmountMinor, &value.Fee.Currency, &value.Capacity); err != nil {
			rows.Close()
			return Configuration{}, fmt.Errorf("scan checkout delivery slot: %w", err)
		}
		value.Country = scope.Country
		result.Slots = append(result.Slots, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Configuration{}, fmt.Errorf("iterate checkout delivery slots: %w", err)
	}
	rows.Close()

	rows, err = provider.pool.Query(ctx, `
		SELECT code,minimum_subtotal_minor,discount_basis_points,maximum_discount_minor,starts_at,ends_at
		FROM commerce.promotions
		WHERE tenant_id=$1 AND country=$2 AND active
		ORDER BY code`, scope.TenantID, scope.Country)
	if err != nil {
		return Configuration{}, fmt.Errorf("load checkout promotions: %w", err)
	}
	for rows.Next() {
		var value Promotion
		if err := rows.Scan(&value.Code, &value.MinimumSubtotal, &value.DiscountBasisPts, &value.MaximumDiscount, &value.StartsAt, &value.EndsAt); err != nil {
			rows.Close()
			return Configuration{}, fmt.Errorf("scan checkout promotion: %w", err)
		}
		value.Country = scope.Country
		result.Promotions = append(result.Promotions, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Configuration{}, fmt.Errorf("iterate checkout promotions: %w", err)
	}
	rows.Close()

	rows, err = provider.pool.Query(ctx, `
		SELECT postal_code,locality FROM commerce.postal_zones
		WHERE tenant_id=$1 AND country=$2 AND active
		ORDER BY postal_code,locality`, scope.TenantID, scope.Country)
	if err != nil {
		return Configuration{}, fmt.Errorf("load checkout postal zones: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		value := PostalZone{Country: scope.Country}
		if err := rows.Scan(&value.PostalCode, &value.Locality); err != nil {
			return Configuration{}, fmt.Errorf("scan checkout postal zone: %w", err)
		}
		result.PostalZones = append(result.PostalZones, value)
	}
	if err := rows.Err(); err != nil {
		return Configuration{}, fmt.Errorf("iterate checkout postal zones: %w", err)
	}
	if !validConfiguration(result) {
		return Configuration{}, ErrInvalidRequest
	}
	return result, nil
}

// PostgresCommercialTermsResolver applies product, then vendor, then plan
// defaults from the active commercial policy in a single consistent query.
type PostgresCommercialTermsResolver struct{ pool *pgxpool.Pool }

func NewPostgresCommercialTermsResolver(pool *pgxpool.Pool) (*PostgresCommercialTermsResolver, error) {
	if pool == nil {
		return nil, ErrCommercialTerms
	}
	return &PostgresCommercialTermsResolver{pool: pool}, nil
}

func (resolver *PostgresCommercialTermsResolver) Ready(ctx context.Context) error {
	var ready bool
	if err := resolver.pool.QueryRow(ctx, `
		SELECT to_regclass('commerce.commercial_policies') IS NOT NULL
		   AND to_regclass('commerce.commercial_vendor_rules') IS NOT NULL
		   AND to_regclass('commerce.commercial_product_rules') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check commercial terms readiness: %w", err)
	}
	if !ready {
		return ErrCommercialTerms
	}
	return nil
}

func (resolver *PostgresCommercialTermsResolver) Resolve(parent context.Context, scope Scope, itemID, variantID, vendorID string) (CommercialTerms, error) {
	if parent == nil || !postgresCheckoutScope(scope) || !checkoutUUID(itemID) || !checkoutUUID(variantID) || !checkoutUUID(vendorID) {
		return CommercialTerms{}, ErrCommercialTerms
	}
	ctx, cancel := context.WithTimeout(parent, checkoutConfigurationTimeout)
	defer cancel()
	var value CommercialTerms
	err := resolver.pool.QueryRow(ctx, `
		SELECT p.version,
		       COALESCE(product.vendor_tier,vendor.vendor_tier,p.default_vendor_tier),
		       COALESCE(product.commission_basis_points,vendor.commission_basis_points,p.default_commission_basis_points),
		       COALESCE(product.wallet_basis_points,vendor.wallet_basis_points,p.default_wallet_basis_points),
		       CASE WHEN product.id IS NOT NULL THEN 'PRODUCT_OVERRIDE'
		            WHEN vendor.id IS NOT NULL THEN 'VENDOR_OVERRIDE'
		            ELSE 'PLAN_DEFAULT' END
		FROM commerce.commercial_policies p
		LEFT JOIN LATERAL (
			SELECT r.id,r.vendor_tier,r.commission_basis_points,r.wallet_basis_points
			FROM commerce.commercial_product_rules r
			WHERE r.policy_id=p.id AND r.vendor_id=$3
			  AND (r.variant_id=$5 OR (r.variant_id IS NULL AND r.item_id=$4))
			ORDER BY (r.variant_id IS NOT NULL) DESC
			LIMIT 1
		) product ON true
		LEFT JOIN commerce.commercial_vendor_rules vendor
		  ON vendor.policy_id=p.id AND vendor.vendor_id=$3
		WHERE p.tenant_id=$1 AND p.country=$2 AND p.active
		ORDER BY p.published_at DESC
		LIMIT 1`, scope.TenantID, scope.Country, vendorID, itemID, variantID).Scan(
		&value.PolicyVersion, &value.VendorTier, &value.CommissionBasisPoints, &value.WalletRedemptionBasisPoints, &value.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommercialTerms{}, ErrCommercialTerms
	}
	if err != nil {
		return CommercialTerms{}, fmt.Errorf("resolve commercial terms: %w", err)
	}
	if !validResolvedTerms(value) {
		return CommercialTerms{}, ErrCommercialTerms
	}
	return value, nil
}
