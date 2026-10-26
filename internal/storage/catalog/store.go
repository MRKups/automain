// Package catalog persists derived passage metadata and durable remote indexing
// receipts in SQLite. It contains no vectors and makes no remote calls.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

var _ domain.ChunkCatalog = (*Store)(nil)

// Open only initializes an empty file. Existing databases must carry this
// catalog's application ID and supported schema, never the legacy vector ID.
func Open(ctx context.Context, path string) (_ *Store, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		return nil, invalid("catalog database path must not be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve catalog database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, fmt.Errorf("create catalog database directory: %w", err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	u.RawQuery = url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(1)"}, "_txlock": {"immediate"}}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open catalog database: %w", err)
	}
	defer func() {
		if retErr != nil {
			if err := db.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close catalog database: %w", err))
			}
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.transaction(ctx, func(tx *sql.Tx) error { return migrate(ctx, tx) }); err != nil {
		return nil, fmt.Errorf("initialize catalog database: %w", err)
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return nil, fmt.Errorf("configure catalog database: %w", err)
		}
	}
	return s, nil
}

func migrate(ctx context.Context, tx *sql.Tx) error {
	var appID, userVersion int
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return fmt.Errorf("read catalog application ID: %w", err)
	}
	if appID != 0 && appID != ApplicationID {
		return invalid("catalog database belongs to another application; use a separate index.db file")
	}
	if appID == 0 {
		var objects int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'").Scan(&objects); err != nil {
			return fmt.Errorf("inspect catalog database ownership: %w", err)
		}
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
			return fmt.Errorf("read database user version: %w", err)
		}
		if objects != 0 || userVersion != 0 {
			return invalid("catalog path points to an existing non-catalog database; use a separate index.db file")
		}
		if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("create catalog schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", ApplicationID)); err != nil {
			return fmt.Errorf("set catalog application ID: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO catalog_schema_migrations VALUES (?, ?)", schemaVersion, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("record catalog schema version: %w", err)
		}
	}
	var version sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(version) FROM catalog_schema_migrations").Scan(&version); err != nil {
		return fmt.Errorf("read catalog schema version: %w", err)
	}
	if !version.Valid {
		return invalid("catalog schema version is missing; inspect the catalog file or restore a valid backup")
	}
	if version.Int64 != schemaVersion {
		return invalid(fmt.Sprintf("unsupported catalog schema version %d; expected %d", version.Int64, schemaVersion))
	}
	return nil
}

func (s *Store) SchemaVersion() int { return schemaVersion }

func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close catalog database: %w", err)
	}
	return nil
}

func (s *Store) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("check catalog database: %w", err)
	}
	return nil
}

func (s *Store) transaction(ctx context.Context, apply func(*sql.Tx) error) (retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			retErr = errors.Join(retErr, fmt.Errorf("rollback catalog transaction: %w", err))
		}
	}()
	if err := apply(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog transaction: %w", err)
	}
	return nil
}

func closeRows(rows *sql.Rows, retErr *error) {
	if err := rows.Close(); err != nil {
		*retErr = errors.Join(*retErr, fmt.Errorf("close catalog rows: %w", err))
	}
}

func invalid(message string) error {
	return domain.NewError(domain.ErrorCodeInvalidInput, "catalog.index", message,
		"Use a separate catalog database and valid current passage/model metadata; prepare and retry stale work.", nil)
}

func validateBatch(repo string, chunks []domain.SearchChunk) error {
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return invalid(err.Error())
	}
	seen := make(map[string]bool, len(chunks))
	for _, chunk := range chunks {
		if err := chunk.Validate(); err != nil {
			return invalid(err.Error())
		}
		if chunk.Repository != repo || seen[chunk.ID] {
			return invalid("chunk repository mismatch or duplicate chunk ID")
		}
		seen[chunk.ID] = true
	}
	return nil
}

func validateMutationSize(size int) error {
	if size < 1 || size > 256 {
		return invalid("catalog receipt mutation batch must contain between 1 and 256 entries")
	}
	return nil
}

func validateMutationBatch(repo string, chunks []domain.SearchChunk) error {
	if err := validateMutationSize(len(chunks)); err != nil {
		return err
	}
	return validateBatch(repo, chunks)
}

func validateScope(repo string, space domain.EmbeddingSpace) error {
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return invalid(err.Error())
	}
	if err := space.Validate(); err != nil {
		return invalid(err.Error())
	}
	return nil
}
