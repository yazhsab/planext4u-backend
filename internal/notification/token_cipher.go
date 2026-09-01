package notification

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

type TokenCipher interface {
	Encrypt(string, string, string) ([]byte, int, error)
	Decrypt(string, string, []byte, int) (string, error)
}

type AESGCMTokenCipher struct {
	keys          map[int][]byte
	activeVersion int
	random        io.Reader
}

func NewAESGCMTokenCipher(keys map[int][]byte, activeVersion int) (*AESGCMTokenCipher, error) {
	if activeVersion < 1 || len(keys) == 0 {
		return nil, ErrInvalidRequest
	}
	cloned := make(map[int][]byte, len(keys))
	for version, key := range keys {
		if version < 1 || len(key) != 32 {
			return nil, ErrInvalidRequest
		}
		cloned[version] = append([]byte(nil), key...)
	}
	if _, exists := cloned[activeVersion]; !exists {
		return nil, ErrInvalidRequest
	}
	return &AESGCMTokenCipher{keys: cloned, activeVersion: activeVersion, random: rand.Reader}, nil
}

func (tokenCipher *AESGCMTokenCipher) Encrypt(tenantID, deviceID, value string) ([]byte, int, error) {
	if !uuidPattern.MatchString(tenantID) || !safeID(deviceID) || len(value) < 20 || len(value) > 4096 {
		return nil, 0, ErrInvalidRequest
	}
	gcm, err := tokenCipher.gcm(tokenCipher.activeVersion)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(tokenCipher.random, nonce); err != nil {
		return nil, 0, fmt.Errorf("generate device-token nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(value), []byte(tenantID+"|"+deviceID))
	return append(nonce, ciphertext...), tokenCipher.activeVersion, nil
}

func (tokenCipher *AESGCMTokenCipher) Decrypt(tenantID, deviceID string, ciphertext []byte, version int) (string, error) {
	if !uuidPattern.MatchString(tenantID) || !safeID(deviceID) {
		return "", ErrInvalidRequest
	}
	gcm, err := tokenCipher.gcm(version)
	if err != nil || len(ciphertext) <= gcm.NonceSize() {
		return "", errors.New("device-token ciphertext is invalid")
	}
	plaintext, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], []byte(tenantID+"|"+deviceID))
	if err != nil || len(plaintext) < 20 || len(plaintext) > 4096 {
		return "", errors.New("decrypt device token")
	}
	return string(plaintext), nil
}

func (tokenCipher *AESGCMTokenCipher) gcm(version int) (cipher.AEAD, error) {
	key, exists := tokenCipher.keys[version]
	if !exists {
		return nil, errors.New("device-token key version is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

var _ TokenCipher = (*AESGCMTokenCipher)(nil)
