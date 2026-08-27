// Package postgres implements the approved PostgreSQL control-plane adapter.
package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.up.sql migrations/*.down.sql
var migrationFiles embed.FS

type MigrationDirection string

const (
	MigrationUp   MigrationDirection = "up"
	MigrationDown MigrationDirection = "down"
)

// Migrate applies ordered, checksummed SQL migrations. A changed applied
// migration is rejected before any new SQL executes.
func Migrate(ctx context.Context, pool *pgxpool.Pool, direction MigrationDirection) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}

	var names []string
	suffix := "." + string(direction) + ".sql"
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), suffix) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if direction == MigrationDown {
		for left, right := 0, len(names)-1; left < right; left, right = left+1, right-1 {
			names[left], names[right] = names[right], names[left]
		}
	}

	for _, name := range names {
		contents, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		version := strings.TrimSuffix(strings.TrimSuffix(name, suffix), ".")
		checksum := checksum(contents)
		if err := applyMigration(ctx, pool, version, checksum, string(contents), direction); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// VerifyCurrentSchema performs the Web/API's read-only startup gate. Runtime
// service credentials never apply migrations because Worker SECURITY DEFINER
// functions have a separate, non-login owner.
func VerifyCurrentSchema(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		version := strings.TrimSuffix(entry.Name(), ".up.sql")
		var observedChecksum string
		err = pool.QueryRow(ctx, "SELECT checksum_sha256 FROM schema_migrations WHERE version=$1", version).Scan(&observedChecksum)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required migration %s is not applied", version)
		}
		if err != nil {
			return fmt.Errorf("read migration %s state: %w", version, err)
		}
		if observedChecksum != checksum(contents) {
			return fmt.Errorf("migration checksum mismatch for %s", version)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, version, checksum, sql string, direction MigrationDirection) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if direction == MigrationUp {
		_, _ = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, checksum_sha256 TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())")
		var existing string
		err = tx.QueryRow(ctx, "SELECT checksum_sha256 FROM schema_migrations WHERE version=$1 FOR UPDATE", version).Scan(&existing)
		if err == nil {
			if existing != checksum {
				return fmt.Errorf("checksum changed for applied migration %s", version)
			}
			return tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version, checksum_sha256) VALUES($1,$2)", version, checksum)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func checksum(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}
