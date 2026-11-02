package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConsentGrantCannotOverwriteANewerWithdrawalOrAnotherCountry(t *testing.T) {
	fixture := newServiceFixture(t)
	login := fixture.exchange(t, "provider-customer", "consent-order-device")
	ctx := context.Background()
	repository := fixture.repository
	grant := Consent{Country: "IN", EvidenceID: "grant-one", Purpose: ConsentLocationDelivery, Granted: true, PolicyVersion: "delivery-1", RecordedAt: time.Now().UTC(), ExpectedVersion: new(int64(0))}
	first, err := repository.RecordConsent(ctx, login.IdentityID, grant, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	withdraw := grant
	withdraw.EvidenceID, withdraw.Granted, withdraw.ExpectedVersion = "withdraw-one", false, nil
	second, err := repository.RecordConsent(ctx, login.IdentityID, withdraw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	grant.ExpectedVersion = &first.Version
	if _, err := repository.RecordConsent(ctx, login.IdentityID, grant, time.Now().UTC()); !errors.Is(err, ErrConsentConflict) {
		t.Fatalf("stale grant overwrote withdrawal: %v", err)
	}
	grant.EvidenceID, grant.ExpectedVersion = "explicit-new-grant", &second.Version
	if _, err := repository.RecordConsent(ctx, login.IdentityID, grant, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if current, err := repository.ListConsents(ctx, login.IdentityID, "SG"); err != nil || len(current) != 0 {
		t.Fatalf("country leak: %#v %v", current, err)
	}
	grant.Country, grant.EvidenceID, grant.ExpectedVersion = "SG", "separate-country-grant", new(int64(0))
	if _, err := repository.RecordConsent(ctx, login.IdentityID, grant, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if current, err := repository.ListConsents(ctx, login.IdentityID, "IN"); err != nil || len(current) != 1 || current[0].EvidenceID != "explicit-new-grant" {
		t.Fatalf("other country overwrote current choice: %#v %v", current, err)
	}
}
