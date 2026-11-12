package bus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// migrateV2 is the supported upgrade from v0.1.0-rc.4. Unreleased intermediate
// schemas deliberately remain rejected rather than being guessed at.
func (s *Store) migrateV2(ctx context.Context) error {
	if s.source != ":memory:" {
		backup, err := os.CreateTemp(filepath.Dir(s.source), filepath.Base(s.source)+".schema2-backup-")
		if err != nil {
			return fmt.Errorf("create migration backup: %w", err)
		}
		path := backup.Name()
		if err := backup.Close(); err != nil {
			return err
		}
		// SQLite's snapshot includes committed WAL state; copying only the db does not.
		if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
			return fmt.Errorf("migration backup %s failed (database unchanged): %w", path, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer s.db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := migrateV2Tx(ctx, tx); err != nil {
		return fmt.Errorf("schema 2 migration rolled back: %w", err)
	}
	return tx.Commit()
}

func migrateV2Tx(ctx context.Context, tx sqlExecutor) error {
	// A write transaction covers renames, copies, validation and the version bump.
	if _, err := tx.ExecContext(ctx, "UPDATE scopes SET created_at=created_at WHERE 0"); err != nil {
		return err
	}
	for _, index := range []string{"agents_scope_updated", "messages_inbox", "messages_sender", "messages_idempotency", "tasks_scope_status", "escalations_scope_status"} {
		if _, err := tx.ExecContext(ctx, "DROP INDEX "+index); err != nil {
			return err
		}
	}
	tables := []string{"scopes", "agents", "peer_links", "messages", "reservations", "tasks", "escalations"}
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" RENAME TO legacy_"+table); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return err
	}
	statements := []string{
		`INSERT INTO scopes(scope_id,token_hash,created_at) SELECT scope_id,token_hash,created_at FROM legacy_scopes`,
		`INSERT INTO agents SELECT * FROM legacy_agents`,
		`INSERT INTO peer_links SELECT * FROM legacy_peer_links`,
		`INSERT INTO messages SELECT message_id,scope_id,'agent',from_agent,'agent',to_agent,mode,body,context_json,response_to,idempotency_key,request_hash,state,reservation_id,created_at,expires_at,delivered_at,acknowledged_at,replied_at,response_message_id FROM legacy_messages`,
		`INSERT INTO reservations SELECT * FROM legacy_reservations`,
		// The old description remains intact; a bounded, stable title is added.
		`INSERT INTO tasks SELECT task_id,scope_id,'Migrated task '||task_id,description,created_by,claimed_by,claimed_execution_id,status,dependencies_json,note,created_at,updated_at FROM legacy_tasks`,
		`INSERT INTO escalations SELECT * FROM legacy_escalations`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for i := len(tables) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, "DROP TABLE legacy_"+tables[i]); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("migrated database contains invalid foreign keys")
	}
	return rows.Err()
}

// migrateV9 expands participant attribution without changing execution credentials
// or outstanding deliveries. Rebuilding only messages preserves its stable name
// for all referencing tables; foreign keys are checked before committing.
func (s *Store) migrateV9(ctx context.Context) error {
	if s.source != ":memory:" {
		backup, err := os.CreateTemp(filepath.Dir(s.source), filepath.Base(s.source)+".schema9-backup-")
		if err != nil {
			return err
		}
		path := backup.Name()
		if err := backup.Close(); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
			return fmt.Errorf("schema 9 backup failed: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer s.db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	start := strings.Index(schemaSQL, "CREATE TABLE messages (")
	end := strings.Index(schemaSQL[start:], "CREATE TABLE reservations (") + start
	ddl := schemaSQL[start:end]
	// Keep the old table until its copy is complete. Do not rename the old table:
	// SQLite would rewrite foreign keys in dependent tables to the temporary name.
	definitionEnd := strings.Index(ddl, "CREATE INDEX")
	create := strings.Replace(ddl[:definitionEnd], "CREATE TABLE messages (", "CREATE TABLE messages_next (", 1)
	if _, err := tx.ExecContext(ctx, create); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages_next SELECT * FROM messages"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE messages; ALTER TABLE messages_next RENAME TO messages;"+ddl[definitionEnd:]+"CREATE INDEX messages_scope_history ON messages(scope_id,created_at DESC,message_id DESC); PRAGMA user_version=10;"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := rows.Next()
	scanErr := rows.Err()
	rows.Close()
	if invalid {
		return fmt.Errorf("schema 9 migration contains invalid foreign keys")
	}
	if scanErr != nil {
		return scanErr
	}
	return tx.Commit()
}

// Schema 10 to 11 is additive. The operational wake tables hold no model
// transcripts or provider credentials and do not alter existing messages.
func (s *Store) migrateV10(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, nativeWakeSchemaSQL+"PRAGMA user_version=11;"); err != nil {
		return err
	}
	return tx.Commit()
}

// Schema 12 adds shared channels without changing existing message rows.
func (s *Store) migrateV11(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, channelsSchemaSQL+"PRAGMA user_version=12;"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateV12(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, attachmentsSchemaSQL+"PRAGMA user_version=13;"); err != nil {
		return err
	}
	return tx.Commit()
}
