package proxypool

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

type Cipher struct {
	aead    cipher.AEAD
	hmacKey []byte
}

func NewCipher(encoded string) (*Cipher, error) {
	master, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(master) != 32 {
		return nil, errors.New("proxy pool master key must be base64-encoded 32 bytes")
	}
	derive := func(info string) ([]byte, error) {
		key := make([]byte, 32)
		_, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte(info)), key)
		return key, err
	}
	enc, err := derive("pansou/proxy-pool/encryption/v1")
	if err != nil {
		return nil, err
	}
	mac, err := derive("pansou/proxy-pool/fingerprint/v1")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(enc)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead, hmacKey: mac}, nil
}

func (c *Cipher) Encrypt(plaintext string) (ciphertext, nonce, fingerprint []byte, err error) {
	if c == nil || c.aead == nil {
		return nil, nil, nil, errors.New("proxy pool cipher is unavailable")
	}
	nonce = make([]byte, c.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, nil, err
	}
	fingerprint = c.Fingerprint([]byte(plaintext))
	ciphertext = c.aead.Seal(nil, nonce, []byte(plaintext), fingerprint)
	return ciphertext, nonce, fingerprint, nil
}

func (c *Cipher) Decrypt(ciphertext, nonce, fingerprint []byte) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("proxy pool cipher is unavailable")
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, fingerprint)
	if err != nil {
		return "", err
	}
	if !hmac.Equal(fingerprint, c.Fingerprint(plaintext)) {
		return "", errors.New("proxy fingerprint mismatch")
	}
	return string(plaintext), nil
}

func (c *Cipher) Fingerprint(value []byte) []byte {
	h := hmac.New(sha256.New, c.hmacKey)
	_, _ = h.Write(value)
	return h.Sum(nil)
}
