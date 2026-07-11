package integration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	accountauth "pansou/auth"
	"pansou/storage"
	"pansou/usage"
)

type IdentityRepository struct {
	Store *storage.Store
}

func (r IdentityRepository) FindUserByNormalizedUsername(ctx context.Context, normalizedUsername string) (accountauth.User, error) {
	user, err := r.Store.GetUserByNormalizedUsername(ctx, normalizedUsername)
	return authUser(user), authStorageError(err)
}

func (r IdentityRepository) FindUserByID(ctx context.Context, userID int64) (accountauth.User, error) {
	user, err := r.Store.GetUserByID(ctx, userID)
	return authUser(user), authStorageError(err)
}

func (r IdentityRepository) FindUserByAPIKeyHash(ctx context.Context, keyHash string) (accountauth.User, error) {
	user, _, err := r.Store.GetUserByAPIKeyHash(ctx, keyHash)
	return authUser(user), authStorageError(err)
}

func (r IdentityRepository) SetPassword(ctx context.Context, userID int64, passwordHash string, mustChange bool) error {
	_, err := r.Store.SetUserPassword(ctx, userID, passwordHash, mustChange, time.Now())
	return authStorageError(err)
}

func (r IdentityRepository) UpdateLastLogin(ctx context.Context, userID int64, at time.Time) error {
	return authStorageError(r.Store.UpdateUserLastLogin(ctx, userID, at))
}

func (r IdentityRepository) UpdateAPIKeyLastUsed(ctx context.Context, userID int64, at time.Time) error {
	return authStorageError(r.Store.UpdateAPIKeyLastUsed(ctx, userID, at))
}

func authUser(user storage.User) accountauth.User {
	return accountauth.User{
		ID:                 user.ID,
		Username:           user.Username,
		NormalizedUsername: user.NormalizedUsername,
		PasswordHash:       user.PasswordHash,
		Role:               user.Role,
		Enabled:            user.Enabled,
		ExpiresAt:          user.ExpiresAt,
		MustChangePassword: user.MustChangePassword,
		AuthVersion:        user.AuthVersion,
		RPSLimit:           user.RPSLimit,
		RPMLimit:           user.RPMLimit,
		RateLimitDisabled:  user.RateLimitDisabled,
		DeletedAt:          user.DeletedAt,
	}
}

func authStorageError(err error) error {
	if err == nil || errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return fmt.Errorf("%w: %v", accountauth.ErrRepositoryUnavailable, err)
}

type UsageRepository struct {
	Store *storage.Store
}

func (r UsageRepository) WriteUsage(ctx context.Context, events []usage.UsageEvent) error {
	inputs := make([]storage.APIRequestLogInput, 0, len(events))
	for _, event := range events {
		userID, err := strconv.ParseInt(event.UserID, 10, 64)
		if err != nil || userID <= 0 {
			return fmt.Errorf("invalid usage user id %q", event.UserID)
		}
		inputs = append(inputs, storage.APIRequestLogInput{
			RequestID:   event.RequestID,
			UserID:      userID,
			AuthType:    metadataString(event.Metadata, "auth_type"),
			Method:      event.Method,
			Endpoint:    event.Route,
			Keyword:     metadataString(event.Metadata, "keyword"),
			StatusCode:  event.StatusCode,
			DurationMS:  event.Duration.Milliseconds(),
			ResultCount: metadataInt(event.Metadata, "result_count"),
			CacheStatus: metadataString(event.Metadata, "cache_status"),
			ErrorCode:   metadataString(event.Metadata, "error_code"),
			SourceIP:    metadataString(event.Metadata, "source_ip"),
			UserAgent:   metadataString(event.Metadata, "user_agent"),
			CreatedAt:   event.OccurredAt,
		})
	}
	_, err := r.Store.InsertAPIRequestLogs(ctx, inputs)
	return err
}

func metadataString(metadata map[string]interface{}, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return value
}

func metadataInt(metadata map[string]interface{}, key string) int {
	if metadata == nil {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
