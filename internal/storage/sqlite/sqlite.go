package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
	_ "modernc.org/sqlite"
)

// Store implements domain.Store backed by a single-owner SQLite database in WAL mode.
type Store struct {
	db            *sql.DB
	path          string
	schemaVersion int
}

// Open initializes a SQLite database at dbPath, configures WAL and foreign-key pragmas,
// and applies any pending forward schema migrations.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("storage.open", err)
	}
	trimmed := strings.TrimSpace(dbPath)
	if trimmed == "" {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"storage.open",
			"Database path (--db) must not be empty.",
			"Pass a writable file path via --db <path> or set AUTOMAIN_DB_PATH.",
			nil,
		)
	}

	cleanPath := filepath.Clean(trimmed)
	if info, statErr := os.Stat(cleanPath); statErr == nil && info.IsDir() {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"storage.open",
			fmt.Sprintf("Database path %q is a directory, not a file.", cleanPath),
			"Pass a file path such as --db /tmp/automain.db.",
			nil,
		)
	}

	parentDir := filepath.Dir(cleanPath)
	if parentDir != "" && parentDir != "." {
		if err := os.MkdirAll(parentDir, 0o750); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"storage.open",
				fmt.Sprintf("Failed to create database directory %q: %v", parentDir, err),
				"Verify directory permissions or pass a writable path via --db <path>.",
				err,
			)
		}
	}

	dsn := cleanPath
	if !strings.Contains(cleanPath, "?") {
		dsn = cleanPath + "?_busy_timeout=5000&_foreign_keys=1&_txlock=immediate"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"storage.open",
			fmt.Sprintf("Failed to open SQLite database at %q: %v", cleanPath, err),
			"Verify the database path and permissions.",
			err,
		)
	}

	// Keep a single pooled connection so connection-scoped pragmas remain active
	// and intra-process writes serialize without SQLITE_BUSY contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	// A vector index must never be opened as the authoritative source store.
	// Source databases predate application IDs and currently use the default 0.
	if err := execPragma(ctx, db, "PRAGMA busy_timeout = 5000;"); err != nil {
		closeDB(db)
		return nil, fmt.Errorf("configure source identity read: %w", err)
	}
	var applicationID int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		closeDB(db)
		return nil, fmt.Errorf("read source database identity: %w", err)
	}
	if applicationID != 0 {
		closeDB(db)
		return nil, domain.NewError(domain.ErrorCodeInvalidInput, "storage.open",
			"Source database path points to a vector index or another application's database.",
			"Use the original source file for --db and a separate file for --vector-db.", nil)
	}

	if err := applyPragmas(ctx, db); err != nil {
		closeDB(db)
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"storage.open",
			fmt.Sprintf("Failed to initialize SQLite pragmas at %q: %v", cleanPath, err),
			"Ensure the database path is writable and points to a valid SQLite file.",
			err,
		)
	}

	version, err := migrate(ctx, db)
	if err != nil {
		closeDB(db)
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"storage.open",
			fmt.Sprintf("Failed to apply SQLite schema migrations at %q: %v", cleanPath, err),
			"Ensure the database file is not corrupted and is writable by the current user.",
			err,
		)
	}

	return &Store{
		db:            db,
		path:          cleanPath,
		schemaVersion: version,
	}, nil
}

// SchemaVersion returns the applied SQLite schema migration version.
func (s *Store) SchemaVersion() int {
	if s == nil {
		return 0
	}
	return s.schemaVersion
}

// Close closes the underlying SQLite database connection pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite store %q: %w", s.path, err)
	}
	return nil
}

func applyPragmas(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
	}
	for _, stmt := range pragmas {
		if err := execPragma(ctx, db, stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt, err)
		}
	}
	return nil
}

func execPragma(ctx context.Context, db *sql.DB, stmt string) error {
	const retryInterval = 5 * time.Millisecond
	const maxWait = 5 * time.Second
	deadline := time.Now().Add(maxWait)
	for {
		_, err := db.ExecContext(ctx, stmt)
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "SQLITE_BUSY") || time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func closeDB(db *sql.DB) {
	if db == nil {
		return
	}
	if err := db.Close(); err != nil {
		return
	}
}

func rollbackTx(tx *sql.Tx) {
	if tx == nil {
		return
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return
	}
}

func closeRows(rows *sql.Rows) {
	if rows == nil {
		return
	}
	if err := rows.Close(); err != nil {
		return
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", raw)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(v int) bool {
	return v != 0
}

func encodeLabels(labels []string) (string, error) {
	if len(labels) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("encode labels json: %w", err)
	}
	return string(data), nil
}

func decodeLabels(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return []string{}, nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(trimmed), &labels); err != nil {
		return nil, fmt.Errorf("decode labels json %q: %w", raw, err)
	}
	if labels == nil {
		return []string{}, nil
	}
	return labels, nil
}
