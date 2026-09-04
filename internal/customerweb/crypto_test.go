package customerweb

import (
	"bytes"
	"strings"
	"testing"
)

func TestTokenCipherUsesAuthenticatedRandomEncryption(t *testing.T) {
	cipher, err := NewTokenCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := cipher.Encrypt("platform-secret-token", "session-1:access")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cipher.Encrypt("platform-secret-token", "session-1:access")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || bytes.Contains(first, []byte("platform-secret-token")) {
		t.Fatal("ciphertext must be randomized and must not expose plaintext")
	}
	plain, err := cipher.Decrypt(first, "session-1:access")
	if err != nil || plain != "platform-secret-token" {
		t.Fatalf("decrypt = %q %v", plain, err)
	}
	if _, err := cipher.Decrypt(first, "session-2:access"); err == nil {
		t.Fatal("associated data mismatch must be rejected")
	}
}
