package storage

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(0x50616e536f75)); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", int64(0x50616e536f75)) }()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		var applied bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)", version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied {
			continue
		}
		script, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if err := applyMigration(ctx, conn.Conn(), version, entry.Name(), string(script)); err != nil {
			return err
		}
	}
	return nil
}

func migrationVersion(name string) (int64, error) {
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		prefix = strings.TrimSuffix(name, filepath.Ext(name))
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("invalid migration filename %q", name)
	}
	return version, nil
}

func applyMigration(ctx context.Context, conn *pgx.Conn, version int64, name, script string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, script); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version, name) VALUES($1,$2)", version, name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}
