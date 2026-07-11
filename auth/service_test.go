package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errNotFound = errors.New("not found")

type fakeRepository struct {
	users      map[int64]User
	byUsername map[string]int64
	byKeyHash  map[string]int64
}

func (f *fakeRepository) FindUserByNormalizedUsername(_ context.Context, username string) (User, error) {
	id, ok := f.byUsername[username]
	if !ok {
		return User{}, errNotFound
	}
	return f.users[id], nil
}

func (f *fakeRepository) FindUserByID(_ context.Context, id int64) (User, error) {
	user, ok := f.users[id]
	if !ok {
		return User{}, errNotFound
	}
	return user, nil
}

func (f *fakeRepository) FindUserByAPIKeyHash(_ context.Context, hash string) (User, error) {
	id, ok := f.byKeyHash[hash]
	if !ok {
		return User{}, errNotFound
	}
	return f.users[id], nil
}

func (f *fakeRepository) SetPassword(_ context.Context, id int64, hash string, mustChange bool) error {
	user := f.users[id]
	user.PasswordHash = hash
	user.MustChangePassword = mustChange
	user.AuthVersion++
	f.users[id] = user
	return nil
}

func (f *fakeRepository) UpdateLastLogin(context.Context, int64, time.Time) error { return nil }
func (f *fakeRepository) UpdateAPIKeyLastUsed(context.Context, int64, time.Time) error {
	return nil
}

func testService(t *testing.T, user User, apiKey string) (*Service, *fakeRepository) {
	t.Helper()
	if user.PasswordHash == "" {
		hash, err := HashPassword("Password!123")
		if err != nil {
			t.Fatal(err)
		}
		user.PasswordHash = hash
	}
	repository := &fakeRepository{
		users:      map[int64]User{user.ID: user},
		byUsername: map[string]int64{user.NormalizedUsername: user.ID},
		byKeyHash:  map[string]int64{},
	}
	if apiKey != "" {
		repository.byKeyHash[HashAPIKey(apiKey)] = user.ID
	}
	service := NewService(repository, "secret", time.Hour)
	service.now = func() time.Time { return time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC) }
	return service, repository
}

func TestLoginAndAuthenticateToken(t *testing.T) {
	user := User{ID: 1, Username: "Alice", NormalizedUsername: "alice", Role: RoleUser, Enabled: true, AuthVersion: 2, RPSLimit: 3, RPMLimit: 60}
	service, _ := testService(t, user, "")
	result, err := service.Login(context.Background(), " ALICE ", "Password!123")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	principal, err := service.AuthenticateToken(context.Background(), result.Token)
	if err != nil {
		t.Fatalf("AuthenticateToken() error = %v", err)
	}
	if principal.UserID != user.ID || principal.Role != RoleUser || principal.RPMLimit != 60 {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestAuthenticateTokenRejectsChangedAuthVersion(t *testing.T) {
	user := User{ID: 1, Username: "Alice", NormalizedUsername: "alice", Role: RoleUser, Enabled: true, AuthVersion: 2}
	service, repository := testService(t, user, "")
	result, err := service.Login(context.Background(), "alice", "Password!123")
	if err != nil {
		t.Fatal(err)
	}
	changed := repository.users[user.ID]
	changed.AuthVersion++
	repository.users[user.ID] = changed
	if _, err := service.AuthenticateToken(context.Background(), result.Token); !errors.Is(err, ErrTokenStale) {
		t.Fatalf("AuthenticateToken() error = %v, want ErrTokenStale", err)
	}
}

func TestAuthenticateAPIKeyRequiresPasswordChangeComplete(t *testing.T) {
	key, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: 1, Username: "Alice", NormalizedUsername: "alice", Role: RoleUser, Enabled: true, MustChangePassword: true}
	service, _ := testService(t, user, key)
	if _, err := service.AuthenticateAPIKey(context.Background(), key); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("AuthenticateAPIKey() error = %v", err)
	}
}

func TestChangePasswordInvalidatesExistingTokens(t *testing.T) {
	user := User{ID: 1, Username: "Alice", NormalizedUsername: "alice", Role: RoleUser, Enabled: true, AuthVersion: 2, MustChangePassword: true}
	service, _ := testService(t, user, "")
	result, err := service.Login(context.Background(), "alice", "Password!123")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(context.Background(), result.Principal, "Password!123", "NewPassword!456"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, err := service.AuthenticateToken(context.Background(), result.Token); !errors.Is(err, ErrTokenStale) {
		t.Fatalf("AuthenticateToken() error = %v, want stale token", err)
	}
}

func TestGenerateAPIKeyRoundTrip(t *testing.T) {
	plain, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidAPIKeyFormat(plain) || prefix == "" || hash != HashAPIKey(plain) {
		t.Fatalf("invalid generated key: prefix=%q hash=%q", prefix, hash)
	}
}

func TestExpiredAndDisabledUsersRejected(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	for _, tt := range []struct {
		name string
		user User
		want error
	}{
		{name: "disabled", user: User{ID: 1, Username: "a", NormalizedUsername: "a", Role: RoleUser, Enabled: false}, want: ErrUserDisabled},
		{name: "expired", user: User{ID: 1, Username: "a", NormalizedUsername: "a", Role: RoleUser, Enabled: true, ExpiresAt: &past}, want: ErrUserExpired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, _ := testService(t, tt.user, "")
			if _, err := service.Login(context.Background(), "a", "Password!123"); !errors.Is(err, tt.want) {
				t.Fatalf("Login() error = %v, want %v", err, tt.want)
			}
		})
	}
}
