package identity

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestContactSnapshotUsesOnlyVerifiedProviderPhoneAndTenantScopedOpaqueDigests(t *testing.T) {
	now := time.Now().UTC()
	key := bytes.Repeat([]byte{7}, 32)
	settings, err := validatedContactSettings(ContactMatchingSettings{HMACKey: key, KeyID: "test-key-1", SnapshotTTL: 24 * time.Hour, MaxContacts: 100})
	if err != nil {
		t.Fatal(err)
	}
	beforeMutation := settings.phoneHash("tenant-1", "+919876543210")
	key[0] = 0
	if settings.phoneHash("tenant-1", "+919876543210") != beforeMutation {
		t.Fatal("caller mutated retained hashing key")
	}
	proof := ProviderAccount{Subject: "immutable-firebase-owner", AuthenticatedAt: now, Methods: []string{"phone"}, VerifiedPhone: "+919876543210"}
	request := ContactSnapshotRequest{ProviderToken: "sensitive-token-not-persisted", PolicyVersion: "contacts-v1", Phones: []string{"+919123456789", "+919876543210", "+919123456789", "+442079460123"}}
	first, err := settings.prepare("tenant-1", proof, request, now)
	if err != nil || len(first.contactHashes) != 2 || len(first.phoneHash) != 64 {
		t.Fatalf("opaque snapshot invalid: %v", err)
	}
	request.Phones = []string{"+442079460123", "+919123456789"}
	second, err := settings.prepare("tenant-1", proof, request, now)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("ordering/duplicates/self phone changed an equivalent snapshot")
	}
	other, err := settings.prepare("tenant-2", proof, request, now)
	if err != nil || first.phoneHash == other.phoneHash || first.requestHash == other.requestHash {
		t.Fatal("phone equality leaked across tenants")
	}
	for _, digest := range append(first.contactHashes, first.phoneHash, first.requestHash) {
		if strings.Contains(digest, "987654") || strings.Contains(digest, "sensitive-token") {
			t.Fatal("plain contact data entered the persistent snapshot")
		}
	}
	if !first.expiresAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatal("snapshot retention was not bounded")
	}
}

func TestContactMatchingRejectsUnverifiedStaleAmbiguousAndOversizedInput(t *testing.T) {
	now := time.Now().UTC()
	settings, _ := validatedContactSettings(ContactMatchingSettings{HMACKey: bytes.Repeat([]byte{1}, 32), KeyID: "test", SnapshotTTL: time.Hour, MaxContacts: 1})
	proof := ProviderAccount{Subject: "immutable-firebase-owner", AuthenticatedAt: now, Methods: []string{"phone"}, VerifiedPhone: "+919876543210"}
	if _, err := settings.prepare("tenant-1", proof, ContactSnapshotRequest{PolicyVersion: "contacts-v1"}, now); err == nil {
		t.Fatal("missing phone array accepted")
	}
	request := ContactSnapshotRequest{PolicyVersion: "contacts-v1", Phones: []string{"+919123456789"}}
	for _, mutate := range []func(*ProviderAccount){func(p *ProviderAccount) { p.VerifiedPhone = "" }, func(p *ProviderAccount) { p.Methods = []string{"password"} }, func(p *ProviderAccount) { p.AuthenticatedAt = now.Add(-6 * time.Minute) }, func(p *ProviderAccount) { p.VerifiedPhone = "9876543210" }} {
		bad := proof
		mutate(&bad)
		if _, err := settings.prepare("tenant-1", bad, request, now); err == nil {
			t.Fatal("unverified/stale provider phone accepted")
		}
	}
	for _, phone := range []string{"9123456789", "+91 9123456789", " +919123456789", "+0123456789", "+919123456789\n"} {
		bad := request
		bad.Phones = []string{phone}
		if _, err := settings.prepare("tenant-1", proof, bad, now); err == nil {
			t.Fatal("ambiguous phone accepted")
		}
	}
	request.Phones = append(request.Phones, "+442079460123")
	if _, err := settings.prepare("tenant-1", proof, request, now); err == nil {
		t.Fatal("oversized snapshot accepted")
	}
}
