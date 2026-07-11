package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const dataMigrationColumns = `migration_key, completed_at, summary`

func scanDataMigration(row rowScanner) (DataMigration, error) {
	var migration DataMigration
	var summary []byte
	err := row.Scan(&migration.MigrationKey, &migration.CompletedAt, &summary)
	if err == nil {
		migration.Summary = decodeStrictJSONObject(summary)
	}
	return migration, err
}

func (s *Store) GetDataMigration(ctx context.Context, migrationKey string) (DataMigration, error) {
	if s == nil || s.pool == nil {
		return DataMigration{}, fmt.Errorf("storage is disabled")
	}
	migration, err := scanDataMigration(s.pool.QueryRow(ctx, `SELECT `+dataMigrationColumns+`
		FROM data_migrations WHERE migration_key=$1`, strings.TrimSpace(migrationKey)))
	if errors.Is(err, pgx.ErrNoRows) {
		return DataMigration{}, ErrNotFound
	}
	if err != nil {
		return DataMigration{}, fmt.Errorf("get data migration: %w", err)
	}
	return migration, nil
}

// ImportPluginCredentialsAndCompleteMigration applies a prepared legacy-data
// import once. Callers must parse, validate, and encrypt all files before this
// transaction starts; no plaintext is accepted by this API.
func (s *Store) ImportPluginCredentialsAndCompleteMigration(ctx context.Context, input ImportPluginCredentialsInput) (DataMigrationResult, error) {
	if s == nil || s.pool == nil {
		return DataMigrationResult{}, fmt.Errorf("storage is disabled")
	}
	key := strings.TrimSpace(input.MigrationKey)
	if key == "" {
		return DataMigrationResult{}, fmt.Errorf("%w: empty data migration key", ErrInvalid)
	}
	summary, err := marshalStrictJSON(input.Summary)
	if err != nil {
		return DataMigrationResult{}, fmt.Errorf("%w: data migration summary: %v", ErrInvalid, err)
	}
	completedAt := input.CompletedAt
	if completedAt.IsZero() {
		completedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DataMigrationResult{}, fmt.Errorf("begin credential data migration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return DataMigrationResult{}, fmt.Errorf("lock credential data migration: %w", err)
	}

	migration, err := scanDataMigration(tx.QueryRow(ctx, `
		INSERT INTO data_migrations (migration_key,completed_at,summary)
		VALUES($1,$2,$3::jsonb) ON CONFLICT (migration_key) DO NOTHING
		RETURNING `+dataMigrationColumns, key, completedAt, summary))
	if errors.Is(err, pgx.ErrNoRows) {
		migration, err = scanDataMigration(tx.QueryRow(ctx, `SELECT `+dataMigrationColumns+`
			FROM data_migrations WHERE migration_key=$1`, key))
		if err != nil {
			return DataMigrationResult{}, fmt.Errorf("read completed data migration: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return DataMigrationResult{}, fmt.Errorf("commit existing data migration: %w", err)
		}
		return DataMigrationResult{DataMigration: migration}, nil
	}
	if err != nil {
		return DataMigrationResult{}, mapWriteError("claim data migration", err)
	}
	for _, credentialInput := range input.Credentials {
		if _, err := s.insertPluginCredentialTx(ctx, tx, credentialInput); err != nil {
			return DataMigrationResult{}, fmt.Errorf("import plugin credential: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return DataMigrationResult{}, fmt.Errorf("commit credential data migration: %w", err)
	}
	return DataMigrationResult{
		DataMigration: migration,
		Applied:       true,
		Imported:      len(input.Credentials),
	}, nil
}
