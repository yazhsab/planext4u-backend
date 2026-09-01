package checkout

import (
	"context"
	"fmt"
)

type CommercialRuleSource string

const (
	CommercialRulePlan    CommercialRuleSource = "PLAN_DEFAULT"
	CommercialRuleVendor  CommercialRuleSource = "VENDOR_OVERRIDE"
	CommercialRuleProduct CommercialRuleSource = "PRODUCT_OVERRIDE"
)

type CommercialTerms struct {
	PolicyVersion               string               `json:"policy_version"`
	VendorTier                  string               `json:"vendor_tier"`
	CommissionBasisPoints       int64                `json:"commission_basis_points"`
	WalletRedemptionBasisPoints int64                `json:"wallet_redemption_basis_points"`
	Source                      CommercialRuleSource `json:"source"`
}

type LineCommercialTerms struct {
	ItemID                      string               `json:"item_id"`
	VariantID                   string               `json:"variant_id"`
	VendorID                    string               `json:"vendor_id"`
	VendorTier                  string               `json:"vendor_tier"`
	CommissionBasisPoints       int64                `json:"commission_basis_points"`
	WalletRedemptionBasisPoints int64                `json:"wallet_redemption_basis_points"`
	Commission                  Money                `json:"commission"`
	WalletRedemptionLimit       Money                `json:"wallet_redemption_limit"`
	Source                      CommercialRuleSource `json:"source"`
}

type CommercialTermsResolver interface {
	Resolve(context.Context, Scope, string, string, string) (CommercialTerms, error)
}

type CommercialTermsPolicy struct {
	TenantID                     string
	Country                      string
	Version                      string
	DefaultVendorTier            string
	DefaultCommissionBasisPoints int64
	DefaultWalletBasisPoints     int64
	Vendors                      []VendorCommercialRule
	Products                     []ProductCommercialRule
}

type VendorCommercialRule struct {
	VendorID                    string
	VendorTier                  string
	CommissionBasisPoints       int64
	WalletRedemptionBasisPoints int64
}

type ProductCommercialRule struct {
	ItemID                      string
	VariantID                   string
	VendorID                    string
	VendorTier                  string
	CommissionBasisPoints       int64
	WalletRedemptionBasisPoints int64
}

type StaticCommercialTermsResolver struct {
	policies map[string]CommercialTermsPolicy
}

func NewStaticCommercialTermsResolver(policies []CommercialTermsPolicy) (*StaticCommercialTermsResolver, error) {
	resolver := &StaticCommercialTermsResolver{policies: make(map[string]CommercialTermsPolicy, len(policies))}
	for _, policy := range policies {
		if !validCommercialPolicy(policy) {
			return nil, ErrCommercialTerms
		}
		key := policy.TenantID + "\x00" + policy.Country
		if _, exists := resolver.policies[key]; exists {
			return nil, ErrCommercialTerms
		}
		policy.Vendors = append([]VendorCommercialRule(nil), policy.Vendors...)
		policy.Products = append([]ProductCommercialRule(nil), policy.Products...)
		resolver.policies[key] = policy
	}
	if len(resolver.policies) == 0 {
		return nil, ErrCommercialTerms
	}
	return resolver, nil
}

func (resolver *StaticCommercialTermsResolver) Resolve(_ context.Context, scope Scope, itemID, variantID, vendorID string) (CommercialTerms, error) {
	if resolver == nil || !validScope(scope) || !safeID(itemID) || !safeID(variantID) || !safeID(vendorID) {
		return CommercialTerms{}, ErrCommercialTerms
	}
	policy, exists := resolver.policies[scope.TenantID+"\x00"+scope.Country]
	if !exists {
		return CommercialTerms{}, ErrCommercialTerms
	}
	terms := CommercialTerms{
		PolicyVersion: policy.Version, VendorTier: policy.DefaultVendorTier,
		CommissionBasisPoints:       policy.DefaultCommissionBasisPoints,
		WalletRedemptionBasisPoints: policy.DefaultWalletBasisPoints, Source: CommercialRulePlan,
	}
	for _, rule := range policy.Vendors {
		if rule.VendorID == vendorID {
			terms.VendorTier, terms.CommissionBasisPoints = rule.VendorTier, rule.CommissionBasisPoints
			terms.WalletRedemptionBasisPoints, terms.Source = rule.WalletRedemptionBasisPoints, CommercialRuleVendor
			break
		}
	}
	for _, rule := range policy.Products {
		if rule.VendorID == vendorID && ((rule.VariantID != "" && rule.VariantID == variantID) || (rule.VariantID == "" && rule.ItemID == itemID)) {
			terms.VendorTier, terms.CommissionBasisPoints = rule.VendorTier, rule.CommissionBasisPoints
			terms.WalletRedemptionBasisPoints, terms.Source = rule.WalletRedemptionBasisPoints, CommercialRuleProduct
			break
		}
	}
	return terms, nil
}

func validCommercialPolicy(policy CommercialTermsPolicy) bool {
	if !safeID(policy.TenantID) || len(policy.Country) != 2 || !safeID(policy.Version) || !safeID(policy.DefaultVendorTier) ||
		!validBasisPoints(policy.DefaultCommissionBasisPoints) || !validBasisPoints(policy.DefaultWalletBasisPoints) {
		return false
	}
	seenVendors, seenProducts := map[string]bool{}, map[string]bool{}
	for _, rule := range policy.Vendors {
		if !safeID(rule.VendorID) || !safeID(rule.VendorTier) || !validBasisPoints(rule.CommissionBasisPoints) || !validBasisPoints(rule.WalletRedemptionBasisPoints) || seenVendors[rule.VendorID] {
			return false
		}
		seenVendors[rule.VendorID] = true
	}
	for _, rule := range policy.Products {
		if !safeID(rule.VendorID) || !safeID(rule.VendorTier) || !validBasisPoints(rule.CommissionBasisPoints) || !validBasisPoints(rule.WalletRedemptionBasisPoints) ||
			(rule.ItemID == "" && rule.VariantID == "") || (rule.ItemID != "" && !safeID(rule.ItemID)) || (rule.VariantID != "" && !safeID(rule.VariantID)) {
			return false
		}
		key := fmt.Sprintf("%s\x00%s\x00%s", rule.VendorID, rule.ItemID, rule.VariantID)
		if seenProducts[key] {
			return false
		}
		seenProducts[key] = true
	}
	return true
}

func validBasisPoints(value int64) bool { return value >= 0 && value <= 10000 }
