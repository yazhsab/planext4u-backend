package configcms

import (
	"errors"
	"testing"
	"time"
)

func TestPublishedContactPolicyIsOptionalUniqueAndResolverSafe(t *testing.T) {
	base := validSnapshot(time.Now().UTC())
	valid := ConsentPolicy{Purpose: "CONTACT_MATCHING", PolicyVersion: "contacts-2026.1", Required: false}
	base.ConsentPolicies = append(base.ConsentPolicies, valid)
	if err := validateSnapshot(base); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []ConsentPolicy{
		{Purpose: "CONTACT_MATCHING", PolicyVersion: "contacts-2026.1", Required: true},
		{Purpose: "CONTACT_MATCHING", PolicyVersion: "../other", Required: false},
		{Purpose: "CONTACT_MATCHING", PolicyVersion: "-leading", Required: false},
		{Purpose: "CONTACT_MATCHING", PolicyVersion: "contacts policy", Required: false},
	} {
		attempt := base
		attempt.ConsentPolicies = []ConsentPolicy{policy}
		if err := validateSnapshot(attempt); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("unsafe policy accepted: %+v %v", policy, err)
		}
	}
	duplicated := base
	duplicated.ConsentPolicies = append([]ConsentPolicy(nil), base.ConsentPolicies...)
	duplicated.ConsentPolicies = append(duplicated.ConsentPolicies, valid)
	if err := validateSnapshot(duplicated); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatal("duplicate contact purpose accepted")
	}
}
