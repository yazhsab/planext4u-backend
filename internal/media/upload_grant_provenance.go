package media

import (
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// inspectConditionalUploadGrant records only the narrow property needed by a
// future version purge. It does not verify an arbitrary URL's AWS signature:
// the configured signer is trusted, and this rejects a signer that did not
// return a structurally signed If-None-Match grant. No URL or signature is
// persisted.
func inspectConditionalUploadGrant(rawURL string, headers map[string]string) UploadGrantProvenance {
	unknown := UploadGrantProvenance{Scheme: GrantProvenanceUnknown}
	conditional := false
	for name, value := range headers {
		if strings.EqualFold(name, "if-none-match") {
			if conditional || value != "*" {
				return unknown
			}
			conditional = true
		}
	}
	if !conditional {
		return unknown
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return unknown
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return unknown
	}
	only := func(name string) (string, bool) {
		values := query[name]
		returnValue := len(values) == 1 && values[0] != ""
		if !returnValue {
			return "", false
		}
		return values[0], true
	}
	algorithm, ok := only("X-Amz-Algorithm")
	if !ok || algorithm != "AWS4-HMAC-SHA256" {
		return unknown
	}
	signature, ok := only("X-Amz-Signature")
	if !ok || len(signature) != 64 {
		return unknown
	}
	if decoded, err := hex.DecodeString(signature); err != nil || len(decoded) != 32 {
		return unknown
	}
	signedHeaders, ok := only("X-Amz-SignedHeaders")
	if !ok {
		return unknown
	}
	hasHost, hasCondition := false, false
	for _, name := range strings.Split(signedHeaders, ";") {
		if name == "host" {
			hasHost = true
		}
		if name == "if-none-match" {
			hasCondition = true
		}
	}
	if !hasHost || !hasCondition {
		return unknown
	}
	signedDate, ok := only("X-Amz-Date")
	if !ok {
		return unknown
	}
	signedAt, err := time.Parse("20060102T150405Z", signedDate)
	if err != nil {
		return unknown
	}
	secondsText, ok := only("X-Amz-Expires")
	if !ok {
		return unknown
	}
	seconds, err := strconv.ParseInt(secondsText, 10, 64)
	if err != nil || seconds < 1 || seconds > 30*60 {
		return unknown
	}
	validUntil := signedAt.UTC().Add(time.Duration(seconds) * time.Second)
	return UploadGrantProvenance{Scheme: GrantProvenanceAWSConditionalSigV4, ValidUntil: &validUntil}
}

func validUploadGrantProvenance(value UploadGrantProvenance) bool {
	switch value.Scheme {
	case GrantProvenanceUnknown:
		return value.ValidUntil == nil
	case GrantProvenanceAWSConditionalSigV4:
		return value.ValidUntil != nil && !value.ValidUntil.IsZero()
	default:
		return false
	}
}
