package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

type Binding struct {
	PublicID, PluginKey, Scope string
	OwnerUserID                *int64
	SecretSchemaVersion        int
	ConfigBinding              []byte
}
type Envelope struct {
	Ciphertext, Nonce, BindingFingerprint, CredentialFingerprint []byte
	KeyVersion                                                   int
}
type Cipher struct {
	aead    cipher.AEAD
	hmacKey []byte
}

func NewCipher(encoded string) (*Cipher, error) {
	master, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(master) != 32 {
		return nil, errors.New("credential master key must be base64-encoded 32 bytes")
	}
	derive := func(info string) ([]byte, error) {
		b := make([]byte, 32)
		_, e := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte(info)), b)
		return b, e
	}
	enc, err := derive("pansou/plugin-credential/encryption/v1")
	if err != nil {
		return nil, err
	}
	mac, err := derive("pansou/plugin-credential/fingerprint/v1")
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
func (c *Cipher) Encrypt(b Binding, plaintext, stableID []byte) (Envelope, error) {
	n := make([]byte, c.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return Envelope{}, e
	}
	bindingFingerprint := c.Fingerprint(b.ConfigBinding)
	aad := bindingBytes(b, bindingFingerprint)
	return Envelope{Ciphertext: c.aead.Seal(nil, n, plaintext, aad), Nonce: n, BindingFingerprint: bindingFingerprint, CredentialFingerprint: c.Fingerprint(stableID), KeyVersion: 1}, nil
}
func (c *Cipher) Decrypt(b Binding, e Envelope) ([]byte, error) {
	if !hmac.Equal(e.BindingFingerprint, c.Fingerprint(b.ConfigBinding)) {
		return nil, errors.New("credential binding mismatch")
	}
	return c.aead.Open(nil, e.Nonce, e.Ciphertext, bindingBytes(b, e.BindingFingerprint))
}
func (c *Cipher) Fingerprint(v []byte) []byte {
	h := hmac.New(sha256.New, c.hmacKey)
	h.Write(v)
	return h.Sum(nil)
}
func bindingBytes(b Binding, configFingerprint []byte) []byte {
	owner := ""
	if b.OwnerUserID != nil {
		owner = fmt.Sprint(*b.OwnerUserID)
	}
	parts := [][]byte{[]byte(b.PublicID), []byte(b.PluginKey), []byte(b.Scope), []byte(owner), configFingerprint}
	out := []byte{}
	for _, p := range parts {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(p)))
		out = append(out, n[:]...)
		out = append(out, p...)
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(b.SecretSchemaVersion))
	return append(out, n[:]...)
}
