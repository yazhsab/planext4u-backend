package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ContactMatchingSettings is deployment-owned. Neither the key nor a phone
// digest is exposed by the matching API. Rotating KeyID requires a new snapshot.
type ContactMatchingSettings struct {
	HMACKey     []byte
	KeyID       string
	SnapshotTTL time.Duration
	MaxContacts int
}

type contactMatchingSettings struct {
	key         []byte
	keyID       string
	ttl         time.Duration
	maxContacts int
}

func validatedContactSettings(input ContactMatchingSettings) (contactMatchingSettings, error) {
	if len(input.HMACKey) < 32 || len(input.HMACKey) > 64 || !contactKeyIDPattern.MatchString(input.KeyID) || input.SnapshotTTL < 15*time.Minute || input.SnapshotTTL > 30*24*time.Hour || input.MaxContacts < 1 || input.MaxContacts > 10000 {
		return contactMatchingSettings{}, ErrInvalidConfiguration
	}
	return contactMatchingSettings{key: append([]byte(nil), input.HMACKey...), keyID: input.KeyID, ttl: input.SnapshotTTL, maxContacts: input.MaxContacts}, nil
}

var (
	contactPhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	contactKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

type ContactMatchingState struct {
	Status        string     `json:"status"`
	PolicyVersion string     `json:"policy_version,omitempty"`
	Revision      int64      `json:"revision"`
	ContactCount  int        `json:"contact_count"`
	MaxContacts   int        `json:"max_contacts"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type ContactMatch struct {
	IdentityID  string `json:"identity_id"`
	DisplayName string `json:"display_name"`
}

type ContactMatches struct {
	SnapshotRevision int64          `json:"snapshot_revision"`
	Items            []ContactMatch `json:"items"`
	NextCursor       string         `json:"next_cursor,omitempty"`
}

type ContactSnapshotRequest struct {
	ProviderToken    string   `json:"provider_token"`
	PolicyVersion    string   `json:"policy_version"`
	ExpectedRevision int64    `json:"expected_revision"`
	Phones           []string `json:"phones"`
}

// These repository-only values contain HMACs and immutable provider bindings.
// They cannot be decoded from a client matching request.
type contactSnapshot struct {
	keyID, phoneHash, policyVersion, requestHash string
	contactHashes                                []string
	expectedRevision                             int64
	expiresAt                                    time.Time
}

func (settings contactMatchingSettings) phoneHash(tenant, phone string) string {
	mac := hmac.New(sha256.New, settings.key)
	_, _ = mac.Write([]byte("planext4u-contact-matching-v1\x00" + tenant + "\x00" + phone))
	return hex.EncodeToString(mac.Sum(nil))
}

// Only an externally verified current provider phone may establish the owner's
// matching claim. Mutable Profile.Phone is deliberately absent from this path.
func (settings contactMatchingSettings) prepare(tenant string, proof ProviderAccount, input ContactSnapshotRequest, now time.Time) (contactSnapshot, error) {
	if !validSafeIdentifier(tenant, 128) || !validProviderAccount(proof) || !contactPhonePattern.MatchString(proof.VerifiedPhone) ||
		proof.AuthenticatedAt.After(now.Add(30*time.Second)) || now.Sub(proof.AuthenticatedAt) > 5*time.Minute {
		return contactSnapshot{}, ErrProviderRejected
	}
	phoneMethod := false
	for _, method := range proof.Methods {
		phoneMethod = phoneMethod || method == "phone"
	}
	if !phoneMethod {
		return contactSnapshot{}, ErrProviderRejected
	}
	if input.ExpectedRevision < 0 || !validSafeIdentifier(input.PolicyVersion, 64) || input.Phones == nil || len(input.Phones) > settings.maxContacts {
		return contactSnapshot{}, ErrInvalidInput
	}
	selfHash := settings.phoneHash(tenant, proof.VerifiedPhone)
	unique := map[string]bool{}
	for _, phone := range input.Phones {
		// Do not guess country codes or normalize ambiguous address-book strings.
		// Native readers supply explicit E.164 values; unsupported entries are
		// disclosed as skipped locally, before any purpose-authorized upload.
		if !contactPhonePattern.MatchString(phone) || strings.TrimSpace(phone) != phone {
			return contactSnapshot{}, ErrInvalidInput
		}
		digest := settings.phoneHash(tenant, phone)
		if digest != selfHash {
			unique[digest] = true
		}
	}
	hashes := make([]string, 0, len(unique))
	for digest := range unique {
		hashes = append(hashes, digest)
	}
	sort.Strings(hashes)
	canonical, _ := json.Marshal(struct {
		KeyID            string
		PhoneHash        string
		PolicyVersion    string
		ExpectedRevision int64
		Contacts         []string
	}{settings.keyID, selfHash, input.PolicyVersion, input.ExpectedRevision, hashes})
	fingerprint := sha256.Sum256(canonical)
	return contactSnapshot{keyID: settings.keyID, phoneHash: selfHash, policyVersion: input.PolicyVersion, requestHash: hex.EncodeToString(fingerprint[:]), contactHashes: hashes, expectedRevision: input.ExpectedRevision, expiresAt: now.UTC().Add(settings.ttl)}, nil
}
