package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBEMedia001CleanLifecycleAndIdempotentCompletion(t *testing.T) {
	t.Parallel()
	service, objects, scanner, now := mediaFixture(t)
	grant, err := service.Presign(context.Background(), "tenant-synthetic", "IN", "customer-synthetic", validRequest())
	if err != nil || grant.Asset.State != StatePendingUpload || grant.UploadURL == "" || grant.Headers["x-amz-checksum-sha256"] == "" {
		t.Fatalf("grant = %#v, %v", grant, err)
	}
	objects.metadata[grant.Asset.ObjectKey] = ObjectMetadata{ContentType: grant.Asset.ContentType, SizeBytes: grant.Asset.SizeBytes, SHA256: grant.Asset.SHA256}
	ready, err := service.Complete(context.Background(), "tenant-synthetic", "customer-synthetic", grant.Asset.ID)
	if err != nil || ready.State != StateReady || ready.ReadyAt == nil || scanner.calls != 1 {
		t.Fatalf("ready = %#v, %v calls=%d", ready, err, scanner.calls)
	}
	again, err := service.Complete(context.Background(), "tenant-synthetic", "customer-synthetic", grant.Asset.ID)
	if err != nil || again.Version != ready.Version || scanner.calls != 1 {
		t.Fatalf("idempotent completion = %#v, %v calls=%d", again, err, scanner.calls)
	}
	if !again.ReadyAt.Equal(*now) {
		t.Fatalf("ready at = %v", again.ReadyAt)
	}
}

func TestBEMedia001ExpiryMismatchMalwareAndIDORDenial(t *testing.T) {
	t.Parallel()
	service, objects, scanner, now := mediaFixture(t)

	expiredGrant, _ := service.Presign(context.Background(), "tenant-synthetic", "IN", "owner-a", validRequest())
	*now = now.Add(11 * time.Minute)
	if _, err := service.Complete(context.Background(), "tenant-synthetic", "owner-a", expiredGrant.Asset.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry error = %v", err)
	}

	*now = now.Add(-11 * time.Minute)
	mismatch, _ := service.Presign(context.Background(), "tenant-synthetic", "IN", "owner-a", validRequest())
	objects.metadata[mismatch.Asset.ObjectKey] = ObjectMetadata{ContentType: "image/png", SizeBytes: mismatch.Asset.SizeBytes, SHA256: mismatch.Asset.SHA256}
	rejected, err := service.Complete(context.Background(), "tenant-synthetic", "owner-a", mismatch.Asset.ID)
	if err != nil || rejected.State != StateRejected || rejected.RejectedCode != "UPLOAD_METADATA_MISMATCH" || !objects.deleted[mismatch.Asset.ObjectKey] {
		t.Fatalf("metadata rejection = %#v, %v", rejected, err)
	}

	infected, _ := service.Presign(context.Background(), "tenant-synthetic", "IN", "owner-a", validRequest())
	objects.metadata[infected.Asset.ObjectKey] = ObjectMetadata{ContentType: infected.Asset.ContentType, SizeBytes: infected.Asset.SizeBytes, SHA256: infected.Asset.SHA256}
	scanner.result = ScanResult{Clean: false, ReasonCode: "MALWARE_DETECTED"}
	rejected, err = service.Complete(context.Background(), "tenant-synthetic", "owner-a", infected.Asset.ID)
	if err != nil || rejected.State != StateRejected || rejected.RejectedCode != "MALWARE_DETECTED" || !objects.deleted[infected.Asset.ObjectKey] {
		t.Fatalf("scan rejection = %#v, %v", rejected, err)
	}
	if _, err := service.Get(context.Background(), "tenant-synthetic", "owner-b", infected.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get error = %v", err)
	}
	if _, err := service.Get(context.Background(), "other-tenant", "owner-a", infected.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant get error = %v", err)
	}
}

func TestBEMedia001ValidationDeletionAndRetryableScan(t *testing.T) {
	t.Parallel()
	service, objects, scanner, _ := mediaFixture(t)
	invalid := validRequest()
	invalid.SizeBytes = 20 << 20
	if _, err := service.Presign(context.Background(), "tenant-synthetic", "IN", "owner-a", invalid); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized error = %v", err)
	}
	grant, _ := service.Presign(context.Background(), "tenant-synthetic", "IN", "owner-a", validRequest())
	objects.metadata[grant.Asset.ObjectKey] = ObjectMetadata{ContentType: grant.Asset.ContentType, SizeBytes: grant.Asset.SizeBytes, SHA256: grant.Asset.SHA256}
	scanner.err = errors.New("synthetic scanner outage")
	if _, err := service.Complete(context.Background(), "tenant-synthetic", "owner-a", grant.Asset.ID); !errors.Is(err, ErrDependency) {
		t.Fatalf("scanner error = %v", err)
	}
	scanner.err, scanner.result = nil, ScanResult{Clean: true}
	if asset, err := service.Complete(context.Background(), "tenant-synthetic", "owner-a", grant.Asset.ID); err != nil || asset.State != StateReady {
		t.Fatalf("retry = %#v, %v", asset, err)
	}
	if err := service.Delete(context.Background(), "tenant-synthetic", "owner-a", grant.Asset.ID); err != nil {
		t.Fatalf("delete = %v", err)
	}
	if _, err := service.Get(context.Background(), "tenant-synthetic", "owner-a", grant.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted get = %v", err)
	}
}

func TestBrowserPresentationsExposeOnlyReadyPublicMedia(t *testing.T) {
	t.Parallel()
	service, objects, _, now := mediaFixture(t)
	publicGrant, err := service.Presign(context.Background(), "tenant-synthetic", "IN", "vendor-a", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	objects.metadata[publicGrant.Asset.ObjectKey] = ObjectMetadata{ContentType: publicGrant.Asset.ContentType, SizeBytes: publicGrant.Asset.SizeBytes, SHA256: publicGrant.Asset.SHA256}
	if _, err := service.Complete(context.Background(), "tenant-synthetic", "vendor-a", publicGrant.Asset.ID); err != nil {
		t.Fatal(err)
	}

	privateRequest := validRequest()
	privateRequest.Purpose, privateRequest.AltText, privateRequest.Width, privateRequest.Height = PurposeAvatar, "", 0, 0
	privateGrant, err := service.Presign(context.Background(), "tenant-synthetic", "IN", "customer-a", privateRequest)
	if err != nil {
		t.Fatal(err)
	}
	objects.metadata[privateGrant.Asset.ObjectKey] = ObjectMetadata{ContentType: privateGrant.Asset.ContentType, SizeBytes: privateGrant.Asset.SizeBytes, SHA256: privateGrant.Asset.SHA256}
	if _, err := service.Complete(context.Background(), "tenant-synthetic", "customer-a", privateGrant.Asset.ID); err != nil {
		t.Fatal(err)
	}

	response, err := service.ResolvePresentations(context.Background(), "tenant-synthetic", "IN", []string{publicGrant.Asset.ID, privateGrant.Asset.ID})
	if err != nil || len(response.Items) != 1 || response.Items[0].AssetID != publicGrant.Asset.ID || response.Items[0].AltText != "Fresh milk bottle" ||
		response.Items[0].Width != 1200 || response.Items[0].Height != 1200 || response.Items[0].ExpiresAt == nil ||
		!response.Items[0].ExpiresAt.Equal(now.Add(5*time.Minute)) || len(response.UnavailableAssetIDs) != 1 || response.UnavailableAssetIDs[0] != privateGrant.Asset.ID {
		t.Fatalf("presentations=%#v err=%v", response, err)
	}
	if !strings.HasPrefix(response.Items[0].URL, "https://media.example/") || response.Items[0].Variants == nil {
		t.Fatalf("unsafe or incomplete presentation=%#v", response.Items[0])
	}
	crossCountry, err := service.ResolvePresentations(context.Background(), "tenant-synthetic", "NG", []string{publicGrant.Asset.ID})
	if err != nil || len(crossCountry.Items) != 0 || len(crossCountry.UnavailableAssetIDs) != 1 {
		t.Fatalf("cross-country presentations=%#v err=%v", crossCountry, err)
	}
}

func TestCatalogImageUploadRequiresAccessibleIntrinsicMetadata(t *testing.T) {
	t.Parallel()
	service, _, _, _ := mediaFixture(t)
	for _, mutate := range []func(*PresignRequest){
		func(value *PresignRequest) { value.AltText = "" },
		func(value *PresignRequest) { value.Width = 0 },
		func(value *PresignRequest) { value.Height = 20000 },
	} {
		request := validRequest()
		mutate(&request)
		if _, err := service.Presign(context.Background(), "tenant-synthetic", "IN", "vendor-a", request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid presentation metadata error=%v", err)
		}
	}
}

func validRequest() PresignRequest {
	return PresignRequest{Purpose: PurposeCatalogImage, ContentType: "image/jpeg", SizeBytes: 4096,
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AltText: "Fresh milk bottle", Width: 1200, Height: 1200}
}

func mediaFixture(t *testing.T) (*Service, *fakeObjects, *fakeScanner, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	objects := &fakeObjects{metadata: map[string]ObjectMetadata{}, deleted: map[string]bool{}}
	scanner := &fakeScanner{result: ScanResult{Clean: true}}
	service, err := NewService(NewMemoryRepository(), fakeSigner{}, objects, scanner, 10*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	service.newID = func() (string, error) {
		sequence++
		return fmt.Sprintf("med_synthetic_asset_%d", sequence), nil
	}
	return service, objects, scanner, &now
}

type fakeSigner struct{}

func (fakeSigner) Present(_ context.Context, key, _ string, expiresAt time.Time) (string, error) {
	return fmt.Sprintf("https://media.example/%s?expires=%d", key, expiresAt.Unix()), nil
}

func (fakeSigner) PresignPut(_ context.Context, key string, metadata ObjectMetadata, _ time.Time) (string, map[string]string, error) {
	return "https://uploads.example.test/" + key, map[string]string{"content-type": metadata.ContentType, "x-amz-checksum-sha256": metadata.SHA256}, nil
}

type fakeObjects struct {
	metadata map[string]ObjectMetadata
	deleted  map[string]bool
}

func (objects *fakeObjects) Inspect(_ context.Context, key string) (ObjectMetadata, error) {
	metadata, exists := objects.metadata[key]
	if !exists {
		return ObjectMetadata{}, errors.New("not uploaded")
	}
	return metadata, nil
}

func (objects *fakeObjects) Delete(_ context.Context, key string) error {
	objects.deleted[key] = true
	return nil
}

type fakeScanner struct {
	result ScanResult
	err    error
	calls  int
}

func (scanner *fakeScanner) Scan(_ context.Context, _ string) (ScanResult, error) {
	scanner.calls++
	return scanner.result, scanner.err
}
