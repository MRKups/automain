// Package vector reads legacy SQLite embeddings for explicit migration only.
// It never initializes an index, writes vectors, or calls a model provider.
package vector

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/MRKups/automain/internal/domain"
	"modernc.org/sqlite"
)

type legacyIndex struct {
	db          *sql.DB
	snapshotDir string
}

var _ domain.LegacyIndex = (*legacyIndex)(nil)

// OpenLegacy copies an existing schema-v1 index and its WAL/SHM without changing
// the originals. Stop the old writer before copying. The caller owns Close,
// which releases the private snapshot used by all reads.
func OpenLegacy(ctx context.Context, path string) (_ domain.LegacyIndex, retErr error) {
	defer func() { retErr = operational("vector.legacy.open", retErr) }()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open legacy vector index: %w", err)
	}
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, invalid("legacy index path must name an existing SQLite file, not a URI or memory database")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve legacy index path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG) {
			return nil, invalidCause("legacy index source path does not name an existing regular file", err)
		}
		return nil, fmt.Errorf("inspect legacy index file; choose an existing schema-v1 vector index: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, invalid("legacy index path must name a nonempty regular file")
	}
	// SQLite locates sidecars beside the resolved database, not a symlink alias.
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve legacy index file; check source path permissions: %w", err)
	}
	// Omitting a hot rollback journal can expose spilled, uncommitted pages as
	// committed data. Do not infer whether a journal is hot or recover originals.
	if _, err := os.Lstat(abs + "-journal"); err == nil {
		return nil, domain.NewError(domain.ErrorCodeExternalUnavailable, "vector.legacy.open",
			"Legacy index has a rollback journal; committed state cannot be read safely.",
			"Preserve the original files. Recover a complete disposable copy with SQLite, or use a consistent backup; never delete the original journal to force migration.", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect legacy rollback journal; check source read permissions: %w", err)
	}
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return nil, fmt.Errorf("resolve temporary directory for legacy snapshot; choose an accessible temporary directory: %w", err)
	}
	snapshotDir, err := os.MkdirTemp(tempRoot, "automain-legacy-")
	if err != nil {
		return nil, fmt.Errorf("create private legacy snapshot; check temporary-directory permissions and free disk space: %w", err)
	}
	defer func() {
		if retErr != nil {
			if err := os.RemoveAll(snapshotDir); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove rejected legacy snapshot %q; remove this private directory after closing the reader: %w", snapshotDir, err))
			}
		}
	}()
	snapshotPath := filepath.Join(snapshotDir, "vectors.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if suffix != "" {
			if _, err := os.Lstat(abs + suffix); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("inspect legacy sidecar %q; check source read permissions: %w", abs+suffix, err)
			}
		}
		if err := copyLegacyFile(ctx, abs+suffix, snapshotPath+suffix); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: snapshotPath}
	// Even mode=ro may create WAL/SHM files. Only this disposable copy is ever
	// opened by SQLite, with SHM creation allowed so committed WAL stays readable.
	u.RawQuery = url.Values{
		"mode":    {"ro"},
		"_pragma": {"busy_timeout(5000)", "query_only(1)", "temp_store(MEMORY)"},
	}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open legacy vector index: %w", err)
	}
	defer func() {
		if retErr != nil {
			if err := db.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close rejected legacy index: %w", err))
			}
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &legacyIndex{db: db, snapshotDir: snapshotDir}
	if err := s.snapshot(ctx, func(tx *sql.Tx) error {
		return validateSchema(ctx, tx)
	}); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *legacyIndex) Close() error {
	var retErr error
	if err := s.db.Close(); err != nil {
		retErr = fmt.Errorf("close legacy vector index: %w", err)
	}
	if err := os.RemoveAll(s.snapshotDir); err != nil {
		retErr = errors.Join(retErr, fmt.Errorf("remove legacy snapshot %q; remove this private directory after closing the reader: %w", s.snapshotDir, err))
	}
	return operational("vector.legacy.close", retErr)
}

func copyLegacyFile(ctx context.Context, source, target string) (retErr error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("copy legacy snapshot: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect legacy snapshot input %q; check source read permissions: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy snapshot input %q is not a regular file; preserve the source and inspect its file type", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("read legacy snapshot input %q; check source read permissions: %w", source, err)
	}
	defer func() {
		if err := in.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close legacy snapshot input %q: %w", source, err))
		}
	}()
	info, err = in.Stat()
	if err != nil {
		return fmt.Errorf("inspect legacy snapshot input %q: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy snapshot input %q is not a regular file; preserve the source and inspect its file type", source)
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create legacy snapshot file; check temporary-directory permissions and free disk space: %w", err)
	}
	defer func() {
		if err := out.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close legacy snapshot output; check temporary storage for I/O errors: %w", err))
		}
	}()
	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("copy legacy snapshot: %w", err)
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			written, err := out.Write(buf[:n])
			if err != nil {
				return fmt.Errorf("write legacy snapshot; check temporary-directory permissions and free disk space: %w", err)
			}
			if written != n {
				return fmt.Errorf("write legacy snapshot; check free disk space: %w", io.ErrShortWrite)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read legacy snapshot input %q; preserve the source and check file integrity: %w", source, readErr)
		}
	}
}

func (s *legacyIndex) snapshot(ctx context.Context, read func(*sql.Tx) error) (retErr error) {
	defer func() { retErr = operational("vector.legacy.read", retErr) }()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin legacy index read snapshot: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			retErr = errors.Join(retErr, fmt.Errorf("close legacy index read snapshot: %w", err))
		}
	}()
	if err := read(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finish legacy index read snapshot: %w", err)
	}
	return nil
}

func validateSchema(ctx context.Context, tx *sql.Tx) error {
	var appID int
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return fmt.Errorf("read legacy index application identity: %w", err)
	}
	if appID != ApplicationID {
		return invalid("legacy index does not have the vector application identity")
	}
	var tables int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table'
		AND name IN ('vector_schema_migrations','chunks','embedding_spaces','embeddings')`).Scan(&tables); err != nil {
		return fmt.Errorf("inspect legacy index schema: %w", err)
	}
	if tables != 4 {
		return invalid("legacy index is missing required schema-v1 tables")
	}
	var count, matchingVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(CASE
		WHEN typeof(version)='integer' AND version=? THEN 1 END) FROM vector_schema_migrations`, schemaVersion).Scan(&count, &matchingVersion); err != nil {
		return schemaError("read legacy index schema version", err)
	}
	if count != 1 || matchingVersion != 1 {
		return invalid(fmt.Sprintf("unsupported legacy index migration history; expected only schema version %d", schemaVersion))
	}
	// Preparing these reads checks required columns even when the tables are empty.
	for _, query := range []string{
		"SELECT version,applied_at FROM vector_schema_migrations LIMIT 0",
		"SELECT " + chunkColumns + " FROM chunks c LIMIT 0",
		"SELECT id,provider,model,revision,dimensions FROM embedding_spaces LIMIT 0",
		"SELECT chunk_id,space_id,content_hash,dimensions,vector FROM embeddings LIMIT 0",
	} {
		if err := withRows(ctx, tx, query, nil, func(*sql.Rows) error { return nil }); err != nil {
			return schemaError("validate legacy index columns", err)
		}
	}
	var invalidIDs int
	if err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM chunks WHERE id IS NULL OR id='') OR
		EXISTS(SELECT 1 FROM embedding_spaces WHERE id IS NULL OR id='')`).Scan(&invalidIDs); err != nil {
		return fmt.Errorf("validate legacy index identifiers: %w", err)
	}
	if invalidIDs != 0 {
		return invalid("legacy passages or embedding spaces have missing identifiers")
	}
	if _, err := readSpaces(ctx, tx); err != nil {
		return err
	}
	var orphan int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM embeddings e
		WHERE NOT EXISTS(SELECT 1 FROM chunks c WHERE c.id=e.chunk_id)
		OR NOT EXISTS(SELECT 1 FROM embedding_spaces s WHERE s.id=e.space_id))`).Scan(&orphan); err != nil {
		return fmt.Errorf("validate legacy embedding references: %w", err)
	}
	if orphan != 0 {
		return invalid("legacy embeddings reference missing passages or unregistered spaces")
	}
	return nil
}

const chunkColumns = `c.id, c.repo, c.item_number, c.item_kind, c.source_kind,
c.comment_id, c.start_byte, c.end_byte, c.title, c.text, c.author, c.url, c.content_hash`

func (s *legacyIndex) ListChunks(ctx context.Context, repo, afterID string, limit int) ([]domain.SearchChunk, error) {
	if err := validatePage(repo, limit); err != nil {
		return nil, err
	}
	out := []domain.SearchChunk{}
	err := s.snapshot(ctx, func(tx *sql.Tx) error {
		return withRows(ctx, tx, "SELECT "+chunkColumns+` FROM chunks c
			WHERE c.repo=? AND c.id>? ORDER BY c.id LIMIT ?`, []any{repo, afterID, limit}, func(rows *sql.Rows) error {
			for rows.Next() {
				var c domain.SearchChunk
				if err := rows.Scan(chunkDest(&c)...); err != nil {
					return fmt.Errorf("decode legacy passage: %w", err)
				}
				if err := validateChunk(c, repo); err != nil {
					return err
				}
				out = append(out, c)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *legacyIndex) Spaces(ctx context.Context) ([]domain.EmbeddingSpace, error) {
	var out []domain.EmbeddingSpace
	err := s.snapshot(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = readSpaces(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func readSpaces(ctx context.Context, tx *sql.Tx) ([]domain.EmbeddingSpace, error) {
	out := []domain.EmbeddingSpace{}
	// Spaces has no public page size, but each database read is bounded.
	afterID := ""
	for {
		count := 0
		err := withRows(ctx, tx, `SELECT id,provider,model,revision,dimensions FROM embedding_spaces
			WHERE id>? ORDER BY id LIMIT 1000`, []any{afterID}, func(rows *sql.Rows) error {
			for rows.Next() {
				var id string
				var space domain.EmbeddingSpace
				if err := rows.Scan(&id, &space.Provider, &space.Model, &space.Revision, &space.Dimensions); err != nil {
					return fmt.Errorf("decode legacy embedding space: %w", err)
				}
				if err := validateSpace(id, space); err != nil {
					return err
				}
				out = append(out, space)
				afterID = id
				count++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if count < 1000 {
			return out, nil
		}
	}
}

func (s *legacyIndex) ReadEmbeddings(ctx context.Context, repo string, space domain.EmbeddingSpace, afterID string, limit int) ([]domain.ChunkEmbedding, error) {
	if err := validatePage(repo, limit); err != nil {
		return nil, err
	}
	if err := space.Validate(); err != nil {
		return nil, invalidCause("requested legacy embedding space is invalid", err)
	}
	out := []domain.ChunkEmbedding{}
	err := s.snapshot(ctx, func(tx *sql.Tx) error {
		var id string
		var stored domain.EmbeddingSpace
		if err := tx.QueryRowContext(ctx, `SELECT id,provider,model,revision,dimensions
			FROM embedding_spaces WHERE id=?`, space.ID()).Scan(&id, &stored.Provider, &stored.Model, &stored.Revision, &stored.Dimensions); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return invalid("requested embedding space is not registered in the legacy index")
			}
			return fmt.Errorf("read requested legacy embedding space: %w", err)
		}
		if err := validateSpace(id, stored); err != nil {
			return err
		}
		if stored != space {
			return invalid("legacy embedding space metadata does not match the requested space")
		}
		// Do not filter hash mismatches out of the join: that would hide corrupt or
		// stale historical rows. The migration owner decides canonical-source skips.
		return withRows(ctx, tx, "SELECT "+chunkColumns+`, e.content_hash,e.dimensions,
			CASE WHEN typeof(e.vector)='blob' AND e.dimensions BETWEEN 1 AND 65536
			AND length(e.vector)=e.dimensions*4 THEN e.vector ELSE NULL END
			FROM chunks c JOIN embeddings e ON e.chunk_id=c.id
			WHERE c.repo=? AND e.space_id=? AND c.id>? ORDER BY c.id LIMIT ?`,
			[]any{repo, id, afterID, limit}, func(rows *sql.Rows) error {
				for rows.Next() {
					var entry domain.ChunkEmbedding
					var hash string
					var dimensions int
					var blob []byte
					if err := rows.Scan(append(chunkDest(&entry.Chunk), &hash, &dimensions, &blob)...); err != nil {
						return fmt.Errorf("decode legacy embedding row: %w", err)
					}
					if err := validateChunk(entry.Chunk, repo); err != nil {
						return err
					}
					if hash != entry.Chunk.ContentHash || dimensions != stored.Dimensions {
						return invalid("legacy embedding fingerprint or dimensions disagree with its passage/space metadata")
					}
					vector, err := decodeVector(blob, dimensions)
					if err != nil {
						return fmt.Errorf("decode legacy passage embedding %q: %w", entry.Chunk.ID, err)
					}
					entry.Vector = vector
					out = append(out, entry)
				}
				return nil
			})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validatePage(repo string, limit int) error {
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return invalidCause("legacy index repository must be formatted as owner/name", err)
	}
	if limit < 1 || limit > 10000 {
		return invalid("legacy index batch limit must be between 1 and 10000")
	}
	return nil
}

func validateChunk(c domain.SearchChunk, repo string) error {
	if err := c.Validate(); err != nil {
		return invalidCause("legacy passage metadata is invalid", err)
	}
	if c.Repository != repo || !utf8.ValidString(c.Text) || !utf8.ValidString(c.Title) {
		return invalid("legacy passage has a mismatched repository or invalid UTF-8 input")
	}
	return nil
}

func validateSpace(id string, space domain.EmbeddingSpace) error {
	if err := space.Validate(); err != nil {
		return invalidCause("legacy embedding space metadata is invalid", err)
	}
	if !utf8.ValidString(space.Provider) || !utf8.ValidString(space.Model) || !utf8.ValidString(space.Revision) || id != space.ID() {
		return invalid("legacy embedding space ID disagrees with its registered metadata")
	}
	return nil
}

func chunkDest(c *domain.SearchChunk) []any {
	return []any{&c.ID, &c.Repository, &c.ItemNumber, &c.ItemKind, &c.SourceKind, &c.CommentID,
		&c.StartByte, &c.EndByte, &c.Title, &c.Text, &c.Author, &c.URL, &c.ContentHash}
}

func decodeVector(blob []byte, dimensions int) ([]float32, error) {
	if dimensions < 1 || dimensions > 65536 || len(blob) != dimensions*4 {
		return nil, invalid("legacy vector byte length disagrees with its declared dimensions")
	}
	vector := make([]float32, dimensions)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4 : i*4+4]))
	}
	if err := domain.ValidateVector(vector, dimensions); err != nil {
		return nil, invalidCause("legacy vector contains non-finite values or has zero norm", err)
	}
	return vector, nil
}

func withRows(ctx context.Context, tx *sql.Tx, query string, args []any, read func(*sql.Rows) error) (retErr error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("read legacy vector index: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close legacy index rows: %w", err))
		}
	}()
	if err := read(rows); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy index rows: %w", err)
	}
	return nil
}

func invalid(message string) error {
	return invalidCause(message, nil)
}

func invalidCause(message string, err error) error {
	return domain.NewError(domain.ErrorCodeInvalidInput, "vector.legacy", message,
		"Choose an existing schema-v1 vector index; preserve the original files and inspect their schema/metadata before retrying migration.", err)
}

func schemaError(operation string, err error) error {
	var sqliteErr *sqlite.Error
	// SQLITE_ERROR from these fixed schema probes means a required table/column
	// is incompatible. Corruption, access failures, and I/O retain operational codes.
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 1 {
		return invalidCause("legacy index does not have the required schema-v1 columns", err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func operational(operation string, err error) error {
	if err == nil {
		return nil
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return err
	}
	return domain.NewError(domain.ErrorCodeExternalUnavailable, operation,
		fmt.Sprintf("Legacy index operation failed without changing its files: %v", err),
		"Check source read permissions, file integrity, and temporary-directory permissions/free disk space. Preserve existing WAL and SHM sidecars, or use a consistent SQLite backup; never remove the original WAL to force migration.", err)
}
