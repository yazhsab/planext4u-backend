package main

import (
	"testing"
	"time"
)

func TestContactMatchingRuntimeConfigurationIsExplicitAndBounded(t *testing.T) {
	values := validIdentityConfig()
	config, err := loadRuntimeConfig(configLookup(values))
	if err != nil || config.contactHMACKeyFile != "" || config.contactSnapshotTTL != 24*time.Hour || config.contactMaxContacts != 10000 {
		t.Fatalf("defaults %v", err)
	}
	values["IDENTITY_PROVIDER_MODE"] = "firebase"
	values["FIREBASE_PROJECT_ID"] = "provider-test-project"
	values["CONTACT_MATCHING_HMAC_KEY_FILE"] = "/private/contact-key"
	if _, err = loadRuntimeConfig(configLookup(values)); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"CONTACT_MATCHING_KEY_ID": "bad/key", "CONTACT_MATCHING_SNAPSHOT_TTL": "31d", "CONTACT_MATCHING_MAX_CONTACTS": "10001", "IDENTITY_PROVIDER_MODE": "http"} {
		copy := map[string]string{}
		for k, v := range values {
			copy[k] = v
		}
		copy[key] = value
		if _, err = loadRuntimeConfig(configLookup(copy)); err == nil {
			t.Fatalf("unsafe %s accepted", key)
		}
	}
}
