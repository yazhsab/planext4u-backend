//go:build integration

package checkout

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

func TestPostgresCheckoutStateSurvivesRestartAndIsTenantIsolated(t *testing.T) {
	databaseURL := os.Getenv("CHECKOUT_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("CHECKOUT_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS commerce CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS commerce CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000002_commerce_roles.up.sql",
		"../../migrations/commerce/000001_commerce.up.sql",
		"../../migrations/commerce/000002_checkout_pricing.up.sql",
		"../../migrations/commerce/000003_customer_address_book.up.sql",
		"../../migrations/commerce/000004_checkout_orchestration.up.sql",
		"../../migrations/commerce/000005_published_commercial_configuration.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	store, err := NewPostgresStore(pool, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: postgresCheckoutTenantOne, Country: "IN", CustomerID: postgresCheckoutCustomerOne}
	if _, err := pool.Exec(ctx, `
		INSERT INTO commerce.pricing_policies
		(id,tenant_id,country,version,product_tax_basis_points,product_tax_treatment,platform_fee_minor,platform_fee_tax_basis_points,wallet_point_value_minor,wallet_mode,quote_ttl_seconds,reservation_ttl_seconds,active,published_at,published_by)
		VALUES ('e262b755-68ef-4c0f-a448-896579bfe8de',$1,'IN','pricing-v1',500,'INCLUSIVE',1000,1800,1,'HYBRID_PAYMENT',600,600,true,$2,$3)`, scope.TenantID, now, postgresCheckoutAdminOne); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO commerce.delivery_slots (id,tenant_id,country,window_start,window_end,fee_minor,currency,capacity,revision)
		VALUES ('242839bc-3e86-4c06-987e-2a780fcac895',$1,'IN',$2::timestamptz + interval '1 hour',$2::timestamptz + interval '3 hour',2500,'INR',25,1)`, scope.TenantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO commerce.promotions (id,tenant_id,country,code,minimum_subtotal_minor,discount_basis_points,maximum_discount_minor,starts_at,ends_at,active)
		VALUES ('90e34ec2-8ef5-41e3-a395-9fc12be0b27b',$1,'IN','SAVE10',10000,1000,5000,$2::timestamptz - interval '1 day',$2::timestamptz + interval '1 day',true)`, scope.TenantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO commerce.postal_zones (id,tenant_id,country,postal_code,locality,active,revision,updated_at,updated_by)
		VALUES ('94236626-689a-44b0-894c-0fbfc545b625',$1,'IN','600001','Chennai',true,1,$2,$3)`, scope.TenantID, now, postgresCheckoutAdminOne); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO commerce.commercial_policies (id,tenant_id,country,version,default_vendor_tier,default_commission_basis_points,default_wallet_basis_points,active,published_at,published_by)
		VALUES ('28cfb09e-2276-4144-87c3-b746a128be4b',$1,'IN','commercial-v1','BASIC',1000,2000,true,$2,$3)`, scope.TenantID, now, postgresCheckoutAdminOne); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO commerce.commercial_product_rules (id,policy_id,vendor_id,item_id,variant_id,vendor_tier,commission_basis_points,wallet_basis_points)
		VALUES ('0e11755a-b914-46f8-9302-cf7873be5acf','28cfb09e-2276-4144-87c3-b746a128be4b',$1,$2,$3,'PREMIUM',750,3000)
	`, postgresCheckoutVendorOne, postgresCheckoutItemOne, postgresCheckoutVariantOne); err != nil {
		t.Fatal(err)
	}
	configuration, err := NewPostgresConfigurationProvider(pool)
	if err != nil || configuration.Ready(ctx) != nil {
		t.Fatalf("configuration provider err=%v", err)
	}
	published, err := configuration.Configuration(ctx, scope)
	if err != nil || len(published.Policies) != 1 || len(published.Slots) != 1 || len(published.Promotions) != 1 || len(published.PostalZones) != 1 {
		t.Fatalf("published configuration=%#v err=%v", published, err)
	}
	commercial, err := NewPostgresCommercialTermsResolver(pool)
	if err != nil || commercial.Ready(ctx) != nil {
		t.Fatalf("commercial resolver err=%v", err)
	}
	terms, err := commercial.Resolve(ctx, scope, postgresCheckoutItemOne, postgresCheckoutVariantOne, postgresCheckoutVendorOne)
	if err != nil || terms.Source != CommercialRuleProduct || terms.CommissionBasisPoints != 750 {
		t.Fatalf("commercial terms=%#v err=%v", terms, err)
	}
	addressSeed := Address{Label: "Home", Line1: "10 Main Road", PostalCode: "600001", Locality: "Chennai", Serviceable: true, Default: true}
	created, replay, err := store.CreateAddress(scope, "checkout-address-create-001", "home-address", addressSeed)
	if err != nil || replay || created.Revision != 1 || !created.Default {
		t.Fatalf("created address=%#v replay=%t err=%v", created, replay, err)
	}
	restarted, _ := NewPostgresStore(pool, func() time.Time { return now })
	replayed, replay, err := restarted.CreateAddress(scope, "checkout-address-create-001", "home-address", addressSeed)
	if err != nil || !replay || replayed.ID != created.ID {
		t.Fatalf("address replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	if _, _, err := restarted.CreateAddress(scope, "checkout-address-create-001", "different-address", addressSeed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("address conflict=%v", err)
	}
	updatedSeed := addressSeed
	updatedSeed.Label = "Primary home"
	updated, replay, err := restarted.UpdateAddress(scope, "checkout-address-update-001", "updated-home", created.ID, 1, updatedSeed)
	if err != nil || replay || updated.Revision != 2 || updated.Label != "Primary home" {
		t.Fatalf("updated address=%#v replay=%t err=%v", updated, replay, err)
	}
	addresses, err := restarted.Addresses(scope)
	if err != nil || len(addresses) != 1 || addresses[0].Revision != 2 {
		t.Fatalf("addresses=%#v err=%v", addresses, err)
	}

	quote := Quote{ID: "ignored", CartRevision: 2, PricingPolicyVersion: "pricing-v1", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute), scope: scope, cartID: postgresCheckoutCartOne}
	savedQuote, replay, err := restarted.SaveQuote(scope, "checkout-quote-postgres-001", "quote-fingerprint", quote)
	if err != nil || replay || savedQuote.ID == "ignored" {
		t.Fatalf("saved quote=%#v replay=%t err=%v", savedQuote, replay, err)
	}
	loadedQuote, found, err := restarted.Quote(scope, savedQuote.ID)
	if err != nil || !found || loadedQuote.ID != savedQuote.ID || loadedQuote.scope != scope {
		t.Fatalf("loaded quote=%#v found=%t err=%v", loadedQuote, found, err)
	}
	replayedQuote, replay, err := restarted.SaveQuote(scope, "checkout-quote-postgres-001", "quote-fingerprint", quote)
	if err != nil || !replay || replayedQuote.ID != savedQuote.ID {
		t.Fatalf("quote replay=%#v replay=%t err=%v", replayedQuote, replay, err)
	}

	place := PlaceResult{
		Quote:       savedQuote,
		Reservation: inventory.Reservation{ID: postgresCheckoutReservationOne},
		Payment:     payment.Payment{ID: postgresCheckoutPaymentOne},
		Order:       order.Order{ID: postgresCheckoutOrderOne},
		WalletDebit: &wallet.LedgerEntry{ID: postgresCheckoutWalletOne},
	}
	workflow := process{scope: scope, reservationID: postgresCheckoutReservationOne, orderID: postgresCheckoutOrderOne, walletDebitID: postgresCheckoutWalletOne}
	savedPlace, replay, err := restarted.SavePlace(scope, "checkout-place-postgres-001", "place-fingerprint", place, workflow)
	if err != nil || replay || savedPlace.Order.ID != postgresCheckoutOrderOne {
		t.Fatalf("saved place=%#v replay=%t err=%v", savedPlace, replay, err)
	}
	loadedProcess, found, err := restarted.Process(postgresCheckoutPaymentOne)
	if err != nil || !found || loadedProcess.scope != scope || loadedProcess.orderID != postgresCheckoutOrderOne {
		t.Fatalf("loaded process=%#v found=%t err=%v", loadedProcess, found, err)
	}
	replayedPlace, replay, err := restarted.SavePlace(scope, "checkout-place-postgres-001", "place-fingerprint", place, workflow)
	if err != nil || !replay || replayedPlace.Payment.ID != postgresCheckoutPaymentOne {
		t.Fatalf("place replay=%#v replay=%t err=%v", replayedPlace, replay, err)
	}

	refill := WalletRefillResult{Offer: wallet.RefillOffer{ID: "refill-100", Country: "IN", Points: 100, Price: wallet.Money{AmountMinor: 10000, Currency: "INR"}, PaymentMethods: []string{"RAZORPAY"}, ExpiresAfter: 365 * 24 * time.Hour}, Payment: payment.Payment{ID: postgresCheckoutPaymentTwo}}
	if _, replay, err := restarted.SaveRefill(scope, "checkout-refill-postgres-01", "refill-fingerprint", refill, refillProcess{scope: scope, offer: refill.Offer}); err != nil || replay {
		t.Fatalf("save refill replay=%t err=%v", replay, err)
	}
	loadedRefill, found, err := restarted.Refill(postgresCheckoutPaymentTwo)
	if err != nil || !found || loadedRefill.scope != scope || loadedRefill.offer.ID != "refill-100" {
		t.Fatalf("loaded refill=%#v found=%t err=%v", loadedRefill, found, err)
	}

	other := Scope{TenantID: postgresCheckoutTenantTwo, Country: "IN", CustomerID: postgresCheckoutCustomerOne}
	otherAddresses, err := restarted.Addresses(other)
	if err != nil || len(otherAddresses) != 0 {
		t.Fatalf("cross tenant addresses=%#v err=%v", otherAddresses, err)
	}
	if _, found, err := restarted.Quote(other, savedQuote.ID); err != nil || found {
		t.Fatalf("cross tenant quote found=%t err=%v", found, err)
	}
	if replay, err := restarted.DeleteAddress(scope, "checkout-address-delete-001", "delete-home", created.ID, 2); err != nil || replay {
		t.Fatalf("delete replay=%t err=%v", replay, err)
	}
	if replay, err := restarted.DeleteAddress(scope, "checkout-address-delete-001", "delete-home", created.ID, 2); err != nil || !replay {
		t.Fatalf("delete replay second=%t err=%v", replay, err)
	}
}

const (
	postgresCheckoutTenantOne      = "afc1e0db-73cf-40b3-9927-33590133da0b"
	postgresCheckoutTenantTwo      = "fac48f89-aefb-49a8-82ad-f9b7d15bf49f"
	postgresCheckoutCustomerOne    = "2da29782-f277-40ba-a526-d153be421243"
	postgresCheckoutCartOne        = "2c48e2c9-d512-466f-8881-ff0048d69873"
	postgresCheckoutReservationOne = "57d5b0f5-368f-41cb-bbf2-47bf0d37983f"
	postgresCheckoutPaymentOne     = "4252e186-3fa5-45f9-b226-a8ec61dfb1c1"
	postgresCheckoutPaymentTwo     = "c34b13b6-77d1-47c2-afd7-7e2723b468ba"
	postgresCheckoutOrderOne       = "c4347f2d-d71e-4310-907c-569cddf72d90"
	postgresCheckoutWalletOne      = "7e12384f-6ea2-43bb-be49-11f8a674fc4d"
	postgresCheckoutAdminOne       = "1905509c-b808-4515-8a1d-d5e617bba8bf"
	postgresCheckoutVendorOne      = "d5c667ab-3019-48f3-be65-bd742fa7bed2"
	postgresCheckoutItemOne        = "45f713f6-a2d2-4a55-84bd-29f4f2afe3ba"
	postgresCheckoutVariantOne     = "74839a1c-64f0-45fc-8afa-e9466ea4bdb3"
)
