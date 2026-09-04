package customerweb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

type TokenCipher struct {
	aead cipher.AEAD
}

func NewTokenCipher(key []byte) (*TokenCipher, error) {
	if len(key) != 32 {
		return nil, ErrInvalidConfiguration
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &TokenCipher{aead: aead}, nil
}

func (tokenCipher *TokenCipher) Encrypt(value, associatedData string) ([]byte, error) {
	if tokenCipher == nil || value == "" || associatedData == "" {
		return nil, ErrInvalidRequest
	}
	nonce := make([]byte, tokenCipher.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return tokenCipher.aead.Seal(nonce, nonce, []byte(value), []byte(associatedData)), nil
}

func (tokenCipher *TokenCipher) Decrypt(value []byte, associatedData string) (string, error) {
	if tokenCipher == nil || len(value) <= tokenCipher.aead.NonceSize() || associatedData == "" {
		return "", ErrAuthenticationRequired
	}
	nonceSize := tokenCipher.aead.NonceSize()
	plain, err := tokenCipher.aead.Open(nil, value[:nonceSize], value[nonceSize:], []byte(associatedData))
	if err != nil {
		return "", errors.New("customer web token ciphertext is invalid")
	}
	return string(plain), nil
}
