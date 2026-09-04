package platformlocale

import "testing"

func TestApprovedLocalesAndResolution(t *testing.T) {
	t.Parallel()
	want := []string{"en", "ta", "hi", "te", "kn", "ml", "mr", "bn", "gu"}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("All() = %v", got)
	}
	for index, locale := range want {
		if got[index] != locale || !IsSupported(locale) {
			t.Fatalf("locale %q was not approved in order: %v", locale, got)
		}
	}
	got[0] = "mutated"
	if All()[0] != Default {
		t.Fatal("All returned mutable shared state")
	}
	if IsSupported("fr") {
		t.Fatal("unsupported locale was accepted")
	}
	if resolved := Resolve("hi", "en", []string{"en", "hi"}); resolved != "hi" {
		t.Fatalf("supported resolution = %q", resolved)
	}
	if resolved := Resolve("fr", "ta", []string{"en", "ta"}); resolved != "ta" {
		t.Fatalf("fallback resolution = %q", resolved)
	}
}
