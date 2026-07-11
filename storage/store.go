package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool            *pgxpool.Pool
	defaultCooldown time.Duration
	now             func() time.Time
}

type Option func(*storeOptions)

type storeOptions struct {
	defaultCooldown time.Duration
	poolConfig      func(*pgxpool.Config)
	now             func() time.Time
}

func WithDefaultCooldown(cooldown time.Duration) Option {
	return func(options *storeOptions) {
		if cooldown >= 0 {
			options.defaultCooldown = cooldown
		}
	}
}

func WithPoolConfig(configure func(*pgxpool.Config)) Option {
	return func(options *storeOptions) { options.poolConfig = configure }
}

// Open connects and applies embedded migrations. An empty databaseURL means
// persistence is disabled and intentionally returns a nil store without error.
func Open(ctx context.Context, databaseURL string, options ...Option) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, nil
	}
	settings := storeOptions{defaultCooldown: 7 * 24 * time.Hour, now: time.Now}
	for _, option := range options {
		option(&settings)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if settings.poolConfig != nil {
		settings.poolConfig(config)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	store := &Store{pool: pool, defaultCooldown: settings.defaultCooldown, now: settings.now}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	return store, nil
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	return s.pool.Ping(ctx)
}

func (s *Store) Health(ctx context.Context) error { return s.Ping(ctx) }

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}
