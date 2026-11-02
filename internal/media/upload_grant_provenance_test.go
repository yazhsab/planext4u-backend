package media

import (
	"strings"
	"testing"
	"time"
)

func TestInspectConditionalUploadGrantRequiresSignedHeaderAndExpiry(t *testing.T) {
	base := "https://uploads.example.test/asset?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20261030T000000Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host%3Bif-none-match&X-Amz-Signature=" + strings.Repeat("a", 64)
	valid := inspectConditionalUploadGrant(base, map[string]string{"If-None-Match": "*"})
	if valid.Scheme != GrantProvenanceAWSConditionalSigV4 || valid.ValidUntil == nil ||
		!valid.ValidUntil.Equal(time.Date(2026, 10, 30, 0, 15, 0, 0, time.UTC)) {
		t.Fatalf("valid conditional provenance: %+v", valid)
	}
	for name, test := range map[string]struct {
		url     string
		headers map[string]string
	}{
		"missing condition":   {base, nil},
		"unsigned condition":  {strings.Replace(base, "host%3Bif-none-match", "host", 1), map[string]string{"if-none-match": "*"}},
		"wrong condition":     {base, map[string]string{"if-none-match": "other"}},
		"duplicate condition": {base, map[string]string{"if-none-match": "*", "If-None-Match": "*"}},
		"malformed signature": {strings.Replace(base, strings.Repeat("a", 64), "not-a-signature", 1), map[string]string{"if-none-match": "*"}},
		"missing signed date": {strings.Replace(base, "20261030T000000Z", "invalid", 1), map[string]string{"if-none-match": "*"}},
		"unbounded expiry":    {strings.Replace(base, "Expires=900", "Expires=7200", 1), map[string]string{"if-none-match": "*"}},
		"duplicate signature": {base + "&X-Amz-Signature=" + strings.Repeat("b", 64), map[string]string{"if-none-match": "*"}},
		"unsigned local mock": {"https://uploads.example.test/asset", map[string]string{"if-none-match": "*"}},
	} {
		t.Run(name, func(t *testing.T) {
			provenance := inspectConditionalUploadGrant(test.url, test.headers)
			if provenance.Scheme != GrantProvenanceUnknown || provenance.ValidUntil != nil {
				t.Fatalf("unsafe grant classified: %+v", provenance)
			}
		})
	}
}
