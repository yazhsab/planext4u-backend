package notification

import (
	"bytes"
	"testing"
)

func TestDeviceTokenCipherIsRandomizedTenantBoundAndVersioned(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x42}, 32)
	tokenCipher, err := NewAESGCMTokenCipher(map[int][]byte{3: key}, 3)
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "d1f47ba2-1ad1-46bf-aa23-2969a9ea656f"
	token := "synthetic-device-token-123456789"
	first, version, err := tokenCipher.Encrypt(tenantID, "device-1", token)
	if err != nil || version != 3 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	second, _, _ := tokenCipher.Encrypt(tenantID, "device-1", token)
	if bytes.Equal(first, second) || bytes.Contains(first, []byte(token)) {
		t.Fatal("device-token encryption is deterministic or contains plaintext")
	}
	actual, err := tokenCipher.Decrypt(tenantID, "device-1", first, version)
	if err != nil || actual != token {
		t.Fatalf("token=%q err=%v", actual, err)
	}
	if _, err := tokenCipher.Decrypt("23bf7434-3643-49df-9928-c9169011f69d", "device-1", first, version); err == nil {
		t.Fatal("ciphertext decrypted under another tenant")
	}
}
