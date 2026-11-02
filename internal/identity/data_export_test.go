package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDataExportIncludesWithdrawalsAndHonestCoverageWithoutCredentials(t *testing.T) {
	t.Parallel()
	f := newServiceFixture(t)
	auth := f.exchange(t, "provider-export", "raw-device-export")
	other := f.exchange(t, "provider-other-export", "raw-other-device")
	trusted := TrustedIdentity{Subject: auth.IdentityID, Session: auth.Session.ID, TenantID: auth.TenantID, Country: auth.Country}
	ctx := context.Background()
	grant, err := f.service.RecordConsent(ctx, trusted, ConsentUpdate{Purpose: ConsentAnalytics, Granted: true, PolicyVersion: "p1", ExpectedVersion: new(int64(0))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RecordConsent(ctx, trusted, ConsentUpdate{Purpose: ConsentAnalytics, Granted: false, PolicyVersion: "p1"}); err != nil {
		t.Fatal(err)
	}
	// An older unknown-country decision remains evidence, not a current grant.
	f.repository.mu.Lock()
	f.repository.consents[auth.IdentityID] = append(f.repository.consents[auth.IdentityID], Consent{EvidenceID: "legacy-export", Purpose: ConsentMarketing, Granted: true, PolicyVersion: "legacy", Version: 1, RecordedAt: grant.RecordedAt})
	f.repository.consents[auth.IdentityID] = append(f.repository.consents[auth.IdentityID], Consent{Country: "NG", EvidenceID: "other-country-export", Purpose: ConsentLocationDelivery, Granted: false, PolicyVersion: "ng-policy", Version: 1, RecordedAt: grant.RecordedAt})
	f.repository.consents[other.IdentityID] = []Consent{{Country: "IN", EvidenceID: "foreign-evidence", Purpose: ConsentMarketing, Version: 1}}
	f.repository.mu.Unlock()
	value, err := f.service.ExportData(ctx, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.ConsentHistory) != 4 || len(value.Consents) != 1 || value.Consents[0].Granted || value.SchemaVersion != 1 || len(value.Coverage.Excluded) == 0 {
		t.Fatalf("incomplete or misleading export: %#v", value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{auth.Tokens.AccessToken, auth.Tokens.RefreshToken, "raw-device-export", "provider-export", "foreign-evidence", other.IdentityID, "refresh_digest", "provider_subject", "device_id"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("export leaked %q", secret)
		}
	}
	if !strings.Contains(string(encoded), `"country":null`) || !strings.Contains(string(encoded), `"country":"NG"`) {
		t.Fatalf("legacy/country evidence lost: %s", encoded)
	}
	wrong := trusted
	wrong.TenantID = "other-tenant"
	if _, err = f.service.ExportData(ctx, wrong); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign tenant export = %v", err)
	}
	if err = f.service.Revoke(ctx, auth.Tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ExportData(ctx, trusted); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked export = %v", err)
	}
}

func TestDataExportDoesNotSilentlyTruncateOversizedHistory(t *testing.T) {
	t.Parallel()
	f := newServiceFixture(t)
	auth := f.exchange(t, "provider-export-budget", "device-export-budget")
	trusted := TrustedIdentity{Subject: auth.IdentityID, Session: auth.Session.ID, TenantID: auth.TenantID, Country: auth.Country}
	f.repository.mu.Lock()
	f.repository.consents[auth.IdentityID] = make([]Consent, maxExportRecords+1)
	f.repository.mu.Unlock()
	if _, err := f.service.ExportData(context.Background(), trusted); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("oversized export = %v", err)
	}
}

func TestDataExportV3IncludesOnlyOwnDeletionRequestsAndPreservesEarlierSchemas(t *testing.T) {
	t.Parallel()
	f := newServiceFixture(t)
	owner := f.exchange(t, "provider-export-v3-owner", "device-export-v3-owner")
	foreign := f.exchange(t, "provider-export-v3-foreign", "device-export-v3-foreign")
	ctx := context.Background()
	if _, err := f.service.RequestDeletion(ctx, trustedFor(owner), "Owner's reason"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestDeletion(ctx, trustedFor(foreign), "Foreign reason"); err != nil {
		t.Fatal(err)
	}
	v3, err := f.service.ExportDataVersion(ctx, trustedFor(owner), 3)
	if err != nil || v3.SchemaVersion != 3 || v3.DeletionHistory == nil || len(*v3.DeletionHistory) != 1 || (*v3.DeletionHistory)[0].Reason != "Owner's reason" || v3.ContactSnapshots == nil {
		t.Fatalf("own lifecycle export = %#v, %v", v3, err)
	}
	encoded, err := json.Marshal(v3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Foreign reason") || strings.Contains(string(encoded), foreign.IdentityID) || strings.Contains(string(encoded), "provider-export-v3-owner") {
		t.Fatalf("lifecycle export leaked foreign or provider data: %s", encoded)
	}
	for _, version := range []int{1, 2} {
		prior, err := f.service.ExportDataVersion(ctx, trustedFor(owner), version)
		if err != nil || prior.DeletionHistory != nil {
			t.Fatalf("schema %d changed: %#v, %v", version, prior, err)
		}
	}
}

func TestDataExportV4MakesPartialOwnerCoverageExplicit(t *testing.T) {
	t.Parallel()
	f := newServiceFixture(t)
	owner := f.exchange(t, "provider-export-v4-owner", "device-export-v4-owner")
	value, err := f.service.ExportDataVersion(context.Background(), trustedFor(owner), 4)
	if err != nil {
		t.Fatal(err)
	}
	if value.SchemaVersion != 4 || value.VendorApplications == nil || value.RiderProfiles == nil || value.DeletionHistory == nil || value.OccupationReferences == nil {
		t.Fatalf("incomplete schema 4: %#v", value)
	}
	for _, section := range []string{"vendor_rider_private_evidence_and_reviews", "vendor_rider_bank_details", "vendor_rider_submission_history", "orders_and_payments", "wallet"} {
		found := false
		for _, excluded := range value.Coverage.Excluded {
			found = found || excluded == section
		}
		if !found {
			t.Fatalf("missing excluded coverage %s: %#v", section, value.Coverage)
		}
	}
	for _, included := range value.Coverage.Included {
		if included == "vendor_and_rider_applications" {
			t.Fatal("schema 4 overstated complete application coverage")
		}
	}
}

func TestDataExportV4RejectsOversizedOwnerRows(t *testing.T) {
	t.Parallel()
	f := newServiceFixture(t)
	owner := f.exchange(t, "provider-export-v4-budget", "device-export-v4-budget")
	value := newDataExportV4(Identity{ID: owner.IdentityID}, Session{Country: owner.Country}, time.Now())
	*value.VendorApplications = make([]ExportVendorApplication, maxExportRecords+1)
	if err := finishDataExport(&value); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("oversized vendor export = %v", err)
	}
	*value.VendorApplications = nil
	*value.RiderProfiles = make([]ExportRiderProfile, maxExportRecords+1)
	if err := finishDataExport(&value); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("oversized rider export = %v", err)
	}
}

type revokingExportRepository struct {
	Repository
	revoke func() error
}

func (repository revokingExportRepository) ExportSnapshot(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	value, err := repository.Repository.ExportSnapshot(ctx, trusted, now)
	if err != nil {
		return DataExport{}, err
	}
	if err = repository.revoke(); err != nil {
		return DataExport{}, err
	}
	return value, nil
}

func TestDataExportDiscardsSnapshotWhenSessionIsRevokedDuringAssembly(t *testing.T) {
	t.Parallel()
	f := newServiceFixture(t)
	auth := f.exchange(t, "provider-export-revoke", "device-export-revoke")
	f.service.repository = revokingExportRepository{Repository: f.repository, revoke: func() error { return f.service.Revoke(context.Background(), auth.Tokens.RefreshToken) }}
	if _, err := f.service.ExportData(context.Background(), trustedFor(auth)); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("late revoked snapshot = %v", err)
	}
}
