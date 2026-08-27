package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileCoverageWeightsStatements(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte("mode: atomic\nexample.go:1.1,2.2 3 1\nexample.go:3.1,4.2 1 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	coverage, err := profileCoverage(path)
	if err != nil {
		t.Fatal(err)
	}
	if coverage != 75 {
		t.Fatalf("coverage = %v, want 75", coverage)
	}
}

func TestProfileCoverageRejectsMalformedOrEmptyProfiles(t *testing.T) {
	t.Parallel()
	for _, contents := range []string{"mode: atomic\n", "mode: atomic\ninvalid\n"} {
		path := filepath.Join(t.TempDir(), "coverage.out")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := profileCoverage(path); err == nil {
			t.Fatalf("profileCoverage(%q) error = nil", contents)
		}
	}
}
