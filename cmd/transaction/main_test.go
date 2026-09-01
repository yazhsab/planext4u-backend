package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestTransactionRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":                          "production",
		"DATABASE_URL":                     "postgres://transaction:secret@database.example/p4u?sslmode=verify-full",
		"TENANT_ID":                        "afc1e0db-73cf-40b3-9927-33590133da0b",
		"SUPPORTED_COUNTRIES":              "IN",
		"WALLET_PROGRAM_FILE":              "/run/config/wallet-program.json",
		"BOOKING_OTP_KEY_FILE":             "/run/secrets/booking-otp-key",
		"EMERGENCY_DATA_KEY_FILE":          "/run/secrets/emergency-data-key",
		"LOCAL_VERTICALS_CONTACT_KEY_FILE": "/run/secrets/local-contact-key",
		"RAZORPAY_KEY_ID":                  "rzp_live_publickey0001",
		"RAZORPAY_API_SECRET_FILE":         "/run/secrets/razorpay-api",
		"RAZORPAY_WEBHOOK_SECRET_FILE":     "/run/secrets/razorpay-webhook",
		"NOTIFICATION_BASE_URL":            "https://notification.internal.example",
		"ORDER_NOTIFICATION_HMAC_KEY_FILE": "/run/secrets/order-notification",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-transaction" || config.service.HTTPAddress != ":8086" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	delete(values, "RAZORPAY_WEBHOOK_SECRET_FILE")
	if _, err := loadRuntimeConfig(lookup); err == nil || !strings.Contains(err.Error(), "RAZORPAY") {
		t.Fatalf("incomplete provider error=%v", err)
	}
}

func TestTransactionDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{service: platformconfig.Config{Environment: platformconfig.EnvironmentProduction}, databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute}
	if _, err := databaseConfig("postgres://transaction:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}

func TestTransactionServiceURLAllowsOnlyPrivateHTTPOutsideDevelopment(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"http://notification:8089", "http://notification.production.planext4u.internal:8089"} {
		if _, err := parseServiceURL(raw, platformconfig.EnvironmentProduction); err != nil {
			t.Fatalf("private service URL %q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://notification.example.com", "http://127.0.0.1:8089", "http://user:secret@notification"} {
		if _, err := parseServiceURL(raw, platformconfig.EnvironmentProduction); err == nil {
			t.Fatalf("public or credentialed HTTP service URL %q accepted", raw)
		}
	}
}

func TestWalletProgramUsesExplicitDurations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wallet.json")
	contents := `{"referral_base_url":"https://planext4u.example/referral","refill_offers":[{"id":"refill-100","country":"IN","points":100,"bonus_points":10,"price":{"amount_minor":10000,"currency":"INR"},"payment_methods":["RAZORPAY"],"expires_after":"8760h"}],"campaigns":[]}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	program, err := loadWalletProgram(path)
	if err != nil || len(program.RefillOffers) != 1 || program.RefillOffers[0].ExpiresAfter != 365*24*time.Hour {
		t.Fatalf("program=%#v err=%v", program, err)
	}
}
