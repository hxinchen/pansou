package util

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims JWT载荷结构
type Claims struct {
	UserID      int64  `json:"user_id,omitempty"`
	Username    string `json:"username"`
	Role        string `json:"role,omitempty"`
	AuthVersion int64  `json:"auth_version,omitempty"`
	jwt.RegisteredClaims
}

// TokenIdentity is the authenticated identity embedded in a database-backed
// user token. The legacy GenerateToken helper remains available below.
type TokenIdentity struct {
	UserID      int64
	Username    string
	Role        string
	AuthVersion int64
}

// GenerateToken 生成JWT token
func GenerateToken(username string, secret string, expiry time.Duration) (string, error) {
	return GenerateUserToken(TokenIdentity{Username: username}, secret, expiry)
}

// GenerateUserToken generates a JWT containing the stable user identity and
// authorization version used by the database-backed account system.
func GenerateUserToken(identity TokenIdentity, secret string, expiry time.Duration) (string, error) {
	if identity.Username == "" {
		return "", errors.New("username cannot be empty")
	}
	if secret == "" {
		return "", errors.New("secret cannot be empty")
	}
	if expiry <= 0 {
		return "", errors.New("expiry must be positive")
	}

	now := time.Now()
	expirationTime := now.Add(expiry)
	claims := &Claims{
		UserID:      identity.UserID,
		Username:    identity.Username,
		Role:        identity.Role,
		AuthVersion: identity.AuthVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "pansou",
			Subject:   identity.Username,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ValidateToken 验证JWT token
func ValidateToken(tokenString string, secret string) (*Claims, error) {
	if tokenString == "" {
		return nil, errors.New("token cannot be empty")
	}
	if secret == "" {
		return nil, errors.New("secret cannot be empty")
	}

	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		// 验证签名算法
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}
