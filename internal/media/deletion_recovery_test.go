package media

import (
	"context"
	"errors"
	"testing"
)

type inspectingDeletion struct {
	ObjectStore
	remove func(context.Context, string) error
}

func (object inspectingDeletion) Delete(ctx context.Context, key string) error {
	return object.remove(ctx, key)
}

func TestPrivateDeletionFencesAccessBeforeObjectCleanupAndRetries(t *testing.T) {
	ctx := context.Background()
	service, objects, _, _ := mediaFixture(t)
	input := validRequest()
	input.Purpose = PurposeIdentityDocument
	input.AltText = ""
	input.Width = 0
	input.Height = 0
	grant, err := service.Presign(ctx, "tenant-synthetic", "IN", "owner-a", input)
	if err != nil {
		t.Fatal(err)
	}
	objects.metadata[grant.Asset.ObjectKey] = ObjectMetadata{ContentType: input.ContentType, SizeBytes: input.SizeBytes, SHA256: input.SHA256}
	if _, err := service.CompleteScoped(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.objects = inspectingDeletion{ObjectStore: objects, remove: func(_ context.Context, _ string) error {
		calls++
		if _, err := service.GetScoped(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("object deletion happened before durable access fence: %v", err)
		}
		if calls == 1 {
			return errors.New("object provider unavailable")
		}
		return nil
	}}
	if err := service.DeleteScoped(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); !errors.Is(err, ErrDependency) {
		t.Fatalf("failed cleanup=%v", err)
	}
	if _, err := service.PrivateAccess(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed cleanup restored private access: %v", err)
	}
	if err := service.DeleteScoped(ctx, "tenant-synthetic", "SG", "owner-a", grant.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-country deletion retry=%v", err)
	}
	if calls != 1 {
		t.Fatal("foreign request reached the object store")
	}
	if err := service.DeleteScoped(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); err != nil || calls != 2 {
		t.Fatalf("scoped cleanup retry=%v calls=%d", err, calls)
	}
}
