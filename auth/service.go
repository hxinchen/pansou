package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"pansou/util"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

var (
	ErrInvalidCredentials      = errors.New("invalid credentials")
	ErrUserDisabled            = errors.New("user disabled")
	ErrUserExpired             = errors.New("user expired")
	ErrUserDeleted             = errors.New("user deleted")
	ErrPasswordChangeRequired  = errors.New("password change required")
	ErrTokenStale              = errors.New("token stale")
	ErrInvalidAPIKey           = errors.New("invalid api key")
	ErrRepositoryUnavailable   = errors.New("authentication repository unavailable")
	ErrPasswordPolicyViolation = errors.New("password policy violation")
)

type User struct {
	ID                 int64
	Username           string
	NormalizedUsername string
	PasswordHash       string
	Role               string
	Enabled            bool
	ExpiresAt          *time.Time
	MustChangePassword bool
	AuthVersion        int64
	RPSLimit           int
	RPMLimit           int
	RateLimitDisabled  bool
	DeletedAt          *time.Time
}

type Repository interface {
	FindUserByNormalizedUsername(ctx context.Context, normalizedUsername string) (User, error)
	FindUserByID(ctx context.Context, userID int64) (User, error)
	FindUserByAPIKeyHash(ctx context.Context, keyHash string) (User, error)
	SetPassword(ctx context.Context, userID int64, passwordHash string, mustChange bool) error
	UpdateLastLogin(ctx context.Context, userID int64, at time.Time) error
	UpdateAPIKeyLastUsed(ctx context.Context, userID int64, at time.Time) error
}

type Principal struct {
	UserID             int64      `json:"user_id"`
	Username           string     `json:"username"`
	Role               string     `json:"role"`
	MustChangePassword bool       `json:"must_change_password"`
	RPSLimit           int        `json:"rps_limit"`
	RPMLimit           int        `json:"rpm_limit"`
	RateLimitDisabled  bool       `json:"rate_limit_disabled"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
}

func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }

type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	Principal Principal
}

type Service struct {
	repository Repository
	jwtSecret  string
	tokenTTL   time.Duration
	now        func() time.Time
}

func NewService(repository Repository, jwtSecret string, tokenTTL time.Duration) *Service {
	return &Service{
		repository: repository,
		jwtSecret:  jwtSecret,
		tokenTTL:   tokenTTL,
		now:        time.Now,
	}
}

func NormalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	if s == nil || s.repository == nil {
		return LoginResult{}, ErrRepositoryUnavailable
	}
	user, err := s.repository.FindUserByNormalizedUsername(ctx, NormalizeUsername(username))
	if err != nil {
		return LoginResult{}, mapRepositoryError(err, ErrInvalidCredentials)
	}
	if err := validateActive(user, s.now()); err != nil {
		return LoginResult{}, err
	}
	if !CheckPassword(user.PasswordHash, password) {
		return LoginResult{}, ErrInvalidCredentials
	}
	now := s.now()
	if err := s.repository.UpdateLastLogin(ctx, user.ID, now); err != nil {
		return LoginResult{}, fmt.Errorf("%w: update last login: %v", ErrRepositoryUnavailable, err)
	}
	token, err := util.GenerateUserToken(util.TokenIdentity{
		UserID:      user.ID,
		Username:    user.Username,
		Role:        user.Role,
		AuthVersion: user.AuthVersion,
	}, s.jwtSecret, s.tokenTTL)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		Token:     token,
		ExpiresAt: now.Add(s.tokenTTL),
		Principal: principalFromUser(user),
	}, nil
}

func (s *Service) AuthenticateToken(ctx context.Context, token string) (Principal, error) {
	if s == nil || s.repository == nil {
		return Principal{}, ErrRepositoryUnavailable
	}
	claims, err := util.ValidateToken(token, s.jwtSecret)
	if err != nil || claims.UserID <= 0 {
		return Principal{}, ErrInvalidCredentials
	}
	user, err := s.repository.FindUserByID(ctx, claims.UserID)
	if err != nil {
		return Principal{}, mapRepositoryError(err, ErrInvalidCredentials)
	}
	if err := validateActive(user, s.now()); err != nil {
		return Principal{}, err
	}
	if user.AuthVersion != claims.AuthVersion {
		return Principal{}, ErrTokenStale
	}
	return principalFromUser(user), nil
}

func (s *Service) AuthenticateAPIKey(ctx context.Context, key string) (Principal, error) {
	if s == nil || s.repository == nil {
		return Principal{}, ErrRepositoryUnavailable
	}
	if !ValidAPIKeyFormat(key) {
		return Principal{}, ErrInvalidAPIKey
	}
	user, err := s.repository.FindUserByAPIKeyHash(ctx, HashAPIKey(key))
	if err != nil {
		return Principal{}, mapRepositoryError(err, ErrInvalidAPIKey)
	}
	if err := validateActive(user, s.now()); err != nil {
		return Principal{}, err
	}
	if user.MustChangePassword {
		return Principal{}, ErrPasswordChangeRequired
	}
	if err := s.repository.UpdateAPIKeyLastUsed(ctx, user.ID, s.now()); err != nil {
		return Principal{}, fmt.Errorf("%w: update api key use: %v", ErrRepositoryUnavailable, err)
	}
	return principalFromUser(user), nil
}

func (s *Service) ChangePassword(ctx context.Context, principal Principal, currentPassword, newPassword string) error {
	if s == nil || s.repository == nil {
		return ErrRepositoryUnavailable
	}
	user, err := s.repository.FindUserByID(ctx, principal.UserID)
	if err != nil {
		return mapRepositoryError(err, ErrInvalidCredentials)
	}
	if err := validateActive(user, s.now()); err != nil {
		return err
	}
	if !CheckPassword(user.PasswordHash, currentPassword) {
		return ErrInvalidCredentials
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.repository.SetPassword(ctx, user.ID, hash, false); err != nil {
		return fmt.Errorf("%w: set password: %v", ErrRepositoryUnavailable, err)
	}
	return nil
}

func validateActive(user User, now time.Time) error {
	if user.DeletedAt != nil {
		return ErrUserDeleted
	}
	if !user.Enabled {
		return ErrUserDisabled
	}
	if user.ExpiresAt != nil && !user.ExpiresAt.After(now) {
		return ErrUserExpired
	}
	return nil
}

func principalFromUser(user User) Principal {
	return Principal{
		UserID:             user.ID,
		Username:           user.Username,
		Role:               user.Role,
		MustChangePassword: user.MustChangePassword,
		RPSLimit:           user.RPSLimit,
		RPMLimit:           user.RPMLimit,
		RateLimitDisabled:  user.RateLimitDisabled,
		ExpiresAt:          user.ExpiresAt,
	}
}

func mapRepositoryError(err, fallback error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrRepositoryUnavailable, err)
	}
	if errors.Is(err, ErrRepositoryUnavailable) {
		return err
	}
	return fallback
}
