package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const APIKeyPrefix = "psk_"

func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 8 {
		return "", ErrPasswordPolicyViolation
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func GenerateAPIKey() (plain, prefix, hash string, err error) {
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return "", "", "", err
	}
	plain = APIKeyPrefix + base64.RawURLEncoding.EncodeToString(random)
	prefix = plain
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return plain, prefix, HashAPIKey(plain), nil
}

func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

func ValidAPIKeyFormat(key string) bool {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, APIKeyPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(key, APIKeyPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == 32
}

func GenerateTemporaryPassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%"
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	result := make([]byte, len(random))
	for i, value := range random {
		result[i] = alphabet[int(value)%len(alphabet)]
	}
	password := string(result)
	if password == "" {
		return "", errors.New("generated empty password")
	}
	return password, nil
}
