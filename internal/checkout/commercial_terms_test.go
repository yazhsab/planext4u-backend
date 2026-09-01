package checkout

import (
	"context"
	"testing"
)

func TestCommercialTermsResolveProductVendorPlanCascade(t *testing.T) {
	t.Parallel()
	scope := Scope{TenantID: "tenant-synthetic-001", Country: "IN", CustomerID: "customer-synthetic-001"}
	resolver, err := NewStaticCommercialTermsResolver([]CommercialTermsPolicy{{
		TenantID: scope.TenantID, Country: scope.Country, Version: "commercial-2026-08", DefaultVendorTier: "LOCAL_BASIC", DefaultCommissionBasisPoints: 1200, DefaultWalletBasisPoints: 150,
		Vendors:  []VendorCommercialRule{{VendorID: "vendor-001", VendorTier: "PAN_INDIA_BRONZE", CommissionBasisPoints: 1100, WalletRedemptionBasisPoints: 2000}},
		Products: []ProductCommercialRule{{ItemID: "item-001", VendorID: "vendor-001", VendorTier: "PAN_INDIA_BRONZE", CommissionBasisPoints: 900, WalletRedemptionBasisPoints: 3000}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	product, err := resolver.Resolve(context.Background(), scope, "item-001", "variant-001", "vendor-001")
	if err != nil || product.Source != CommercialRuleProduct || product.CommissionBasisPoints != 900 || product.WalletRedemptionBasisPoints != 3000 {
		t.Fatalf("product terms=%#v err=%v", product, err)
	}
	vendor, err := resolver.Resolve(context.Background(), scope, "item-002", "variant-002", "vendor-001")
	if err != nil || vendor.Source != CommercialRuleVendor || vendor.CommissionBasisPoints != 1100 || vendor.WalletRedemptionBasisPoints != 2000 {
		t.Fatalf("vendor terms=%#v err=%v", vendor, err)
	}
	plan, err := resolver.Resolve(context.Background(), scope, "item-003", "variant-003", "vendor-002")
	if err != nil || plan.Source != CommercialRulePlan || plan.CommissionBasisPoints != 1200 || plan.WalletRedemptionBasisPoints != 150 {
		t.Fatalf("plan terms=%#v err=%v", plan, err)
	}
}

func TestWorkbookCommercialModelProducesAuditableQuote(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	resolver, err := NewStaticCommercialTermsResolver([]CommercialTermsPolicy{{
		TenantID: fixture.scope.TenantID, Country: fixture.scope.Country, Version: "commercial-2026-08", DefaultVendorTier: "LOCAL_BASIC", DefaultCommissionBasisPoints: 1200, DefaultWalletBasisPoints: 150,
		Vendors: []VendorCommercialRule{{VendorID: "vendor-local-001", VendorTier: "PAN_INDIA_BRONZE", CommissionBasisPoints: 1200, WalletRedemptionBasisPoints: 2000}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.deps.CommercialTerms = resolver
	fixture.service.config.Policies[0] = PricingPolicy{
		Version: "pricing-2026-08", Country: "IN", ProductTaxBasisPoints: 1800, ProductTaxTreatment: ProductTaxInclusive,
		PlatformFeeMinor: 5000, PlatformFeeTaxBasisPoints: 1800, WalletPointValueMinor: 100, WalletMode: WalletHybrid,
		QuoteTTL: fixture.service.config.Policies[0].QuoteTTL, ReservationTTL: fixture.service.config.Policies[0].ReservationTTL,
	}

	quote, _, err := fixture.service.Quote(context.Background(), fixture.scope, "idem-workbook-quote-0001", QuoteRequest{CartRevision: 1, AddressID: "address-home-001", DeliverySlot: "slot-standard-001", PromotionCode: "LOCAL10", WalletPoints: 50})
	if err != nil {
		t.Fatal(err)
	}
	if quote.ProductTax.AmountMinor != 3240 || quote.PlatformFee.AmountMinor != 5000 || quote.PlatformFeeTax.AmountMinor != 900 || quote.DeliveryFee.AmountMinor != 1000 ||
		quote.MarketplaceCommission.AmountMinor != 2400 || quote.WalletRedemptionLimit.AmountMinor != 3600 || quote.WalletPointsRedeemed != 36 || quote.WalletApplied.AmountMinor != 3600 || quote.Total.AmountMinor != 21300 ||
		quote.PricingPolicyVersion != "pricing-2026-08" || quote.CommercialPolicyVersion != "commercial-2026-08" || !containsString(quote.Warnings, "WALLET_REDEMPTION_CAPPED_BY_COMMERCIAL_POLICY") {
		t.Fatalf("workbook quote=%#v", quote)
	}
}
