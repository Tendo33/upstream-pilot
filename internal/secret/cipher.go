package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const version = "v1:"

type Cipher struct {
	aead cipher.AEAD
}

func New(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plaintext, purpose string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("create nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(purpose))
	return version + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *Cipher) Decrypt(ciphertext, purpose string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if len(ciphertext) < len(version) || ciphertext[:len(version)] != version {
		return "", errors.New("unsupported ciphertext version")
	}
	raw, err := base64.RawURLEncoding.DecodeString(ciphertext[len(version):])
	if err != nil || len(raw) < c.aead.NonceSize() {
		return "", errors.New("invalid ciphertext")
	}
	nonce, payload := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, payload, []byte(purpose))
	if err != nil {
		return "", errors.New("ciphertext authentication failed")
	}
	return string(plain), nil
}
