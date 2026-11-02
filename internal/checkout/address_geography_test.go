package checkout

import (
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestAddressGeographyRequiresOwningReferenceStoreAndBindsIdempotency(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t, WalletHybrid)
	input := AddressInput{Label: "Home", Line1: "123 Local Road", PostalCode: "600001", Locality: "Chennai"}
	legacy := addressFingerprint(input)
	input.GeographyAreaID = uuid.NewString()
	if legacy == addressFingerprint(input) {
		t.Fatal("geography change reused address fingerprint")
	}
	if _, _, err := f.service.CreateAddress(f.scope, "synthetic-geography-create", input); !errors.Is(err, ErrGeographyUnavailable) {
		t.Fatalf("unverified reference: %v", err)
	}
	if _, _, err := f.service.UpdateAddress(f.scope, "synthetic-geography-update", "address-home-001", 1, input); !errors.Is(err, ErrGeographyUnavailable) {
		t.Fatalf("unverified update: %v", err)
	}
	input.GeographyAreaID = "invalid"
	if validAddressInput(input) {
		t.Fatal("invalid reference id accepted")
	}
	if _, err := NewAuthenticatedPostgresStore(nil, time.Now); err == nil {
		t.Fatal("missing store accepted")
	}
	input.GeographyAreaID = ""
	input.Label = strings.Repeat("அ", 40)
	if !validAddressInput(input) {
		t.Fatal("Tamil address text was measured as UTF-8 bytes")
	}
	input.Line1 = "Invalid\nStreet"
	if validAddressInput(input) {
		t.Fatal("address control text accepted")
	}
}
