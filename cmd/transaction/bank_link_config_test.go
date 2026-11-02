package main

import "testing"

func TestHostedBankLinkConfigurationIsExplicitAndRequiresIndependentResolver(t *testing.T) {
	values := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://local:local@127.0.0.1/test", "TENANT_ID": "afc1e0db-73cf-40b3-9927-33590133da0b", "BOOKING_OTP_KEY_FILE": "/private/booking", "EMERGENCY_DATA_KEY_FILE": "/private/emergency", "LOCAL_VERTICALS_CONTACT_KEY_FILE": "/private/contacts", "BANK_VERIFICATION_BASE_URL": "https://verification.example.test", "BANK_VERIFICATION_TOKEN_FILE": "/private/bank-resolver"}
	lookup := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	for _, test := range []struct {
		endpoint, token, origins string
		allowed                  bool
	}{
		{"", "", "", true},
		{"https://provider.example.test", "/private/link", `["https://bank.example.test"]`, true},
		{"http://127.0.0.1:18081", "/private/link", `["http://localhost:18081"]`, true},
		{"https://provider.example.test", "", `["https://bank.example.test"]`, false},
		{"", "/private/link", `["https://bank.example.test"]`, false},
		{"https://provider.example.test", "/private/link", `[]`, false},
		{"https://provider.example.test", "/private/link", `null`, false},
		{"https://provider.example.test", "/private/link", `["https://bank.example.test/path"]`, false},
		{"http://provider.example.test", "/private/link", `["https://bank.example.test"]`, false},
		{"https://provider.example.test", "/private/link", `["http://bank.example.test"]`, false},
	} {
		values["BANK_LINK_BASE_URL"], values["BANK_LINK_TOKEN_FILE"], values["BANK_LINK_LAUNCH_ORIGINS"] = test.endpoint, test.token, test.origins
		_, err := loadRuntimeConfig(lookup)
		if (err == nil) != test.allowed {
			t.Fatalf("config endpoint=%q allowed=%v err=%v", test.endpoint, test.allowed, err)
		}
	}
	values["BANK_LINK_BASE_URL"], values["BANK_LINK_TOKEN_FILE"], values["BANK_LINK_LAUNCH_ORIGINS"] = "https://provider.example.test", "/private/link", `["https://bank.example.test"]`
	delete(values, "BANK_VERIFICATION_BASE_URL")
	delete(values, "BANK_VERIFICATION_TOKEN_FILE")
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("linking activated without independent bank proof resolver")
	}
}
