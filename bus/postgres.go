package bus

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/october-dev/october-bus/deploy/hosted/migrations"
)

// OpenPostgres opens a migrated standalone Bus database. It never performs DDL
// on service startup. Use MigratePostgres during an explicit deployment step.
func OpenPostgres(ctx context.Context, connectionString string) (*Runtime, error) {
	return OpenPostgresWithOptions(ctx, connectionString, RuntimeOptions{})
}

func OpenPostgresWithOptions(ctx context.Context, connectionString string, options RuntimeOptions) (*Runtime, error) {
	limits, err := normalizedA2APrincipalLimits(options)
	if err != nil {
		return nil, err
	}
	db, err := postgresDatabase(ctx, connectionString)
	if err != nil {
		return nil, err
	}
	if err := checkPostgresMigrations(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return openWithStorage(&Store{db: db}, limits), nil
}

func postgresDatabase(ctx context.Context, connectionString string) (*sqlDatabase, error) {
	if strings.TrimSpace(connectionString) == "" {
		return nil, errors.New("PostgreSQL connection is not configured")
	}
	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		// Driver errors can contain the connection string/password.
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	if err := validatePostgresTLS(config.Host, config.TLSConfig); err != nil {
		return nil, err
	}
	// Validate each host, including remote fallbacks from a local primary.
	for _, fallback := range config.Fallbacks {
		if err := validatePostgresTLS(fallback.Host, fallback.TLSConfig); err != nil {
			return nil, err
		}
	}
	config.RuntimeParams["search_path"] = "october_bus,pg_catalog"
	config.RuntimeParams["application_name"] = "october-bus"
	config.RuntimeParams["default_transaction_isolation"] = "read committed"
	config.RuntimeParams["statement_timeout"] = "30000"
	config.RuntimeParams["lock_timeout"] = "10000"
	config.ConnectTimeout = 10 * time.Second
	// Avoid session-bound prepared statements with transaction-pooling hosts.
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("cannot connect to PostgreSQL; check credentials, TLS and network access")
	}
	return &sqlDatabase{DB: db, backend: StorageBackendPostgres}, nil
}

func validatePostgresTLS(host string, config *tls.Config) error {
	if strings.HasPrefix(host, "/") || strings.EqualFold(host, "localhost") {
		return nil
	}
	if address := net.ParseIP(host); address != nil && address.IsLoopback() {
		return nil
	}
	if config == nil || config.InsecureSkipVerify || config.ServerName == "" {
		return errors.New("remote PostgreSQL requires certificate verification (sslmode=verify-full and a trusted CA)")
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && path.Ext(entry.Name()) == ".sql" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func migrationDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func checkPostgresMigrations(ctx context.Context, db *sqlDatabase) error {
	rows, err := db.QueryContext(ctx, "SELECT name,sha256 FROM schema_migrations ORDER BY name")
	if err != nil {
		return errors.New("PostgreSQL schema is unavailable; run october-bus database migrate before starting the service")
	}
	defer rows.Close()
	names, err := migrationNames()
	if err != nil {
		return err
	}
	index := 0
	for rows.Next() {
		var name, digest string
		if err := rows.Scan(&name, &digest); err != nil {
			return err
		}
		if index >= len(names) || name != names[index] {
			return errors.New("PostgreSQL migration history does not match this binary")
		}
		data, _ := migrations.Files.ReadFile(name)
		if digest != migrationDigest(data) {
			return fmt.Errorf("PostgreSQL migration checksum differs: %s", name)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(names) {
		return errors.New("PostgreSQL schema requires migration before starting this binary")
	}
	return nil
}

// MigratePostgres applies additive, checksum-pinned migrations in one transaction.
// It neither creates a database nor modifies schemas owned by other products.
func MigratePostgres(ctx context.Context, connectionString string) error {
	db, err := postgresDatabase(ctx, connectionString)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(6863746, 1)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS october_bus;
REVOKE ALL ON SCHEMA october_bus FROM PUBLIC;
CREATE TABLE IF NOT EXISTS october_bus.schema_migrations(name TEXT PRIMARY KEY,sha256 TEXT NOT NULL,applied_at TIMESTAMPTZ NOT NULL DEFAULT now());`); err != nil {
		return errors.New("cannot initialize PostgreSQL migration ledger; verify database owner permissions")
	}
	names, err := migrationNames()
	if err != nil {
		return err
	}
	var applied int
	for _, name := range names {
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		var recorded string
		err = tx.QueryRowContext(ctx, "SELECT sha256 FROM schema_migrations WHERE name=?", name).Scan(&recorded)
		if err == nil {
			if recorded != migrationDigest(data) {
				return fmt.Errorf("PostgreSQL migration checksum differs: %s", name)
			}
			applied++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("PostgreSQL migration %s failed; all changes rolled back", name)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(name,sha256) VALUES(?,?)", name, migrationDigest(data)); err != nil {
			return err
		}
		applied++
	}
	var actual int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&actual); err != nil {
		return err
	}
	if actual != applied {
		return errors.New("database has migrations newer than this binary; refusing downgrade")
	}
	// Supabase may grant API roles access by default. This service's schema is
	// server-only: no anonymous/authenticated PostgREST access to message rows.
	if _, err := tx.ExecContext(ctx, `REVOKE ALL ON ALL TABLES IN SCHEMA october_bus FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='anon') THEN
    REVOKE ALL ON SCHEMA october_bus FROM anon;
    REVOKE ALL ON ALL TABLES IN SCHEMA october_bus FROM anon;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='authenticated') THEN
    REVOKE ALL ON SCHEMA october_bus FROM authenticated;
    REVOKE ALL ON ALL TABLES IN SCHEMA october_bus FROM authenticated;
  END IF;
END $$;`); err != nil {
		return errors.New("cannot restrict hosted schema access; migration rolled back")
	}
	return tx.Commit()
}
