package media

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrivateAccessRequiresReadyAssetOwnerTenantAndCountry(t *testing.T) {
	ctx := context.Background()
	service, objects, scanner, now := mediaFixture(t)
	input := validRequest()
	input.Purpose = PurposeIdentityDocument
	input.AltText = ""
	input.Width = 0
	input.Height = 0
	grant, err := service.Presign(ctx, "tenant-synthetic", "IN", "owner-a", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.PrivateAccess(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	objects.metadata[grant.Asset.ObjectKey] = ObjectMetadata{ContentType: input.ContentType, SizeBytes: input.SizeBytes, SHA256: input.SHA256}
	if _, err = service.CompleteScoped(ctx, "tenant-synthetic", "NG", "owner-a", grant.Asset.ID); !errors.Is(err, ErrNotFound) || scanner.calls != 0 {
		t.Fatalf("cross-country mutation: %v calls=%d", err, scanner.calls)
	}
	scanner.err = errors.New("scanner offline")
	if _, err = service.CompleteScoped(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
	if _, err = service.PrivateAccess(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("scan failure published private evidence")
	}
	scanner.err = nil
	if _, err = service.CompleteScoped(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][3]string{{"other-tenant", "IN", "owner-a"}, {"tenant-synthetic", "NG", "owner-a"}, {"tenant-synthetic", "IN", "owner-b"}} {
		if _, err = service.PrivateAccess(ctx, scope[0], scope[1], scope[2], grant.Asset.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("private scope accepted: %v %v", scope, err)
		}
		if err = service.DeleteScoped(ctx, scope[0], scope[1], scope[2], grant.Asset.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete scope accepted: %v %v", scope, err)
		}
	}
	access, err := service.PrivateAccess(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID)
	if err != nil || access.URL == "" || access.ExpiresAt.Sub(*now) != 2*time.Minute {
		t.Fatalf("access=%+v err=%v", access, err)
	}
	if err = service.DeleteScoped(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PrivateAccess(ctx, "tenant-synthetic", "IN", "owner-a", grant.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted private access accepted")
	}
}
