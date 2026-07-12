package auth

import (
	"errors"
	"testing"
)

func TestHashPasswordRequiresEightCharactersOnly(t *testing.T) {
	for _, password := range []string{"abcdefgh", "密码密码密码密码"} {
		hash, err := HashPassword(password)
		if err != nil {
			t.Fatalf("HashPassword() rejected 8-character password %q: %v", password, err)
		}
		if !CheckPassword(hash, password) {
			t.Fatalf("CheckPassword() did not accept password %q", password)
		}
	}
	for _, password := range []string{"abcdefg", "密码密码密码密"} {
		if _, err := HashPassword(password); !errors.Is(err, ErrPasswordPolicyViolation) {
			t.Fatalf("HashPassword(%q) error = %v, want ErrPasswordPolicyViolation", password, err)
		}
	}
}
