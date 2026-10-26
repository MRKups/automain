package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/MRKups/automain/internal/domain"
)

const chunkColumns = `c.id, c.repo, c.item_number, c.item_kind, c.source_kind,
c.comment_id, c.start_byte, c.end_byte, c.title, c.text, c.author, c.url, c.content_hash`

func chunkDest(c *domain.SearchChunk) []any {
	return []any{&c.ID, &c.Repository, &c.ItemNumber, &c.ItemKind, &c.SourceKind, &c.CommentID,
		&c.StartByte, &c.EndByte, &c.Title, &c.Text, &c.Author, &c.URL, &c.ContentHash}
}

func readChunks(rows *sql.Rows) (out []domain.SearchChunk, retErr error) {
	defer closeRows(rows, &retErr)
	out = []domain.SearchChunk{}
	for rows.Next() {
		var chunk domain.SearchChunk
		if err := rows.Scan(chunkDest(&chunk)...); err != nil {
			return nil, fmt.Errorf("read catalog passage: %w", err)
		}
		if err := chunk.Validate(); err != nil {
			return nil, fmt.Errorf("invalid stored catalog passage %q: %w", chunk.ID, err)
		}
		out = append(out, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read catalog passages: %w", err)
	}
	return out, nil
}

// ReplaceChunks atomically replaces one complete repository snapshot. Entries
// deliberately have no chunk foreign key: even unconfirmed remote writes need cleanup.
func (s *Store) ReplaceChunks(ctx context.Context, repo string, chunks []domain.SearchChunk) error {
	if err := validateBatch(repo, chunks); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current := make(map[string]string, len(chunks))
		for _, chunk := range chunks {
			var owner string
			err := tx.QueryRowContext(ctx, "SELECT repo FROM chunks WHERE id=?", chunk.ID).Scan(&owner)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("check catalog passage ownership: %w", err)
			}
			if err == nil && owner != repo {
				return invalid("chunk ID already belongs to another repository")
			}
			current[chunk.ID] = chunk.ContentHash
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+chunkColumns+" FROM chunks c WHERE c.repo=?", repo)
		if err != nil {
			return fmt.Errorf("list prior catalog passages: %w", err)
		}
		previous, err := readChunks(rows)
		if err != nil {
			return err
		}
		for _, old := range previous {
			hash, exists := current[old.ID]
			if exists && hash == old.ContentHash {
				continue
			}
			query := "UPDATE index_entries SET state='delete' WHERE repo=? AND chunk_id=?"
			args := []any{repo, old.ID}
			if exists {
				query += " AND content_hash<>?"
				args = append(args, hash)
			}
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("tombstone outdated catalog entries: %w", err)
			}
			if !exists {
				if _, err := tx.ExecContext(ctx, "DELETE FROM chunks WHERE repo=? AND id=?", repo, old.ID); err != nil {
					return fmt.Errorf("remove catalog passage: %w", err)
				}
			}
		}
		for _, c := range chunks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO chunks
                (id,repo,item_number,item_kind,source_kind,comment_id,start_byte,end_byte,title,text,author,url,content_hash)
                VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
                ON CONFLICT(id) DO UPDATE SET
                    item_number=excluded.item_number,item_kind=excluded.item_kind,
                    source_kind=excluded.source_kind,comment_id=excluded.comment_id,
                    start_byte=excluded.start_byte,end_byte=excluded.end_byte,
                    title=excluded.title,text=excluded.text,author=excluded.author,
                    url=excluded.url,content_hash=excluded.content_hash`,
				c.ID, c.Repository, c.ItemNumber, string(c.ItemKind), c.SourceKind, c.CommentID,
				c.StartByte, c.EndByte, c.Title, c.Text, c.Author, c.URL, c.ContentHash); err != nil {
				return fmt.Errorf("save catalog passage: %w", err)
			}
		}
		return nil
	})
}

func validatePage(repo string, limit int) error {
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return invalid(err.Error())
	}
	if limit < 1 || limit > 10000 {
		return invalid("catalog page limit must be between 1 and 10000")
	}
	return nil
}

func (s *Store) ListChunks(ctx context.Context, repo, afterID string, limit int) ([]domain.SearchChunk, error) {
	if err := validatePage(repo, limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+chunkColumns+" FROM chunks c WHERE c.repo=? AND c.id>? ORDER BY c.id LIMIT ?", repo, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list catalog passages: %w", err)
	}
	return readChunks(rows)
}

func (s *Store) GetChunk(ctx context.Context, repo, id string) (domain.SearchChunk, error) {
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return domain.SearchChunk{}, invalid(err.Error())
	}
	if strings.TrimSpace(id) == "" {
		return domain.SearchChunk{}, invalid("catalog passage ID must not be empty")
	}
	var chunk domain.SearchChunk
	err := s.db.QueryRowContext(ctx, "SELECT "+chunkColumns+" FROM chunks c WHERE c.repo=? AND c.id=?", repo, id).Scan(chunkDest(&chunk)...)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SearchChunk{}, domain.NewError(domain.ErrorCodeNotFound, "catalog.get",
			fmt.Sprintf("passage %q was not found in repository %q", id, repo),
			"Prepare the repository and select a current passage ID.", err)
	}
	if err != nil {
		return domain.SearchChunk{}, fmt.Errorf("get catalog passage: %w", err)
	}
	if err := chunk.Validate(); err != nil {
		return domain.SearchChunk{}, fmt.Errorf("invalid stored catalog passage %q: %w", id, err)
	}
	return chunk, nil
}

func (s *Store) PendingChunks(ctx context.Context, repo string, space domain.EmbeddingSpace, limit int) ([]domain.SearchChunk, error) {
	if err := validateScope(repo, space); err != nil {
		return nil, err
	}
	if err := validatePage(repo, limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+chunkColumns+` FROM chunks c
        WHERE c.repo=? AND NOT EXISTS (
            SELECT 1 FROM index_entries e WHERE e.repo=c.repo AND e.chunk_id=c.id
            AND e.content_hash=c.content_hash AND e.space_id=? AND e.state='indexed'
        ) ORDER BY c.item_number,c.id LIMIT ?`, repo, space.ID(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending catalog passages: %w", err)
	}
	return readChunks(rows)
}

func (s *Store) IndexedChunks(ctx context.Context, repo string, space domain.EmbeddingSpace, afterID string, limit int) ([]domain.SearchChunk, error) {
	if err := validateScope(repo, space); err != nil {
		return nil, err
	}
	if err := validatePage(repo, limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+chunkColumns+` FROM chunks c
        JOIN index_entries e ON e.repo=c.repo AND e.chunk_id=c.id AND e.content_hash=c.content_hash
        WHERE c.repo=? AND e.space_id=? AND e.state='indexed' AND c.id>?
        ORDER BY c.id LIMIT ?`, repo, space.ID(), afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list indexed catalog passages: %w", err)
	}
	return readChunks(rows)
}
