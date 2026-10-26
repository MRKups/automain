package catalog

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/MRKups/automain/internal/domain"
)

func registerSpace(ctx context.Context, tx *sql.Tx, space domain.EmbeddingSpace) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_spaces (id,provider,model,revision,dimensions)
        VALUES (?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		space.ID(), space.Provider, space.Model, space.Revision, space.Dimensions); err != nil {
		return fmt.Errorf("register catalog embedding space: %w", err)
	}
	return nil
}

func (s *Store) RegisterSpace(ctx context.Context, space domain.EmbeddingSpace) error {
	if err := space.Validate(); err != nil {
		return invalid(err.Error())
	}
	return s.transaction(ctx, func(tx *sql.Tx) error { return registerSpace(ctx, tx, space) })
}

// TrackChunks durably records possible remote writes before they occur. Repeated
// tracking preserves confirmed receipts. A delete marker can be revived only
// after the single mutation owner has completed remote deletion cleanup.
func (s *Store) TrackChunks(ctx context.Context, repo string, space domain.EmbeddingSpace, chunks []domain.SearchChunk) error {
	if err := validateScope(repo, space); err != nil {
		return err
	}
	if err := validateMutationBatch(repo, chunks); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := registerSpace(ctx, tx, space); err != nil {
			return err
		}
		for _, chunk := range chunks {
			result, err := tx.ExecContext(ctx, `INSERT INTO index_entries
                (repo,space_id,chunk_id,content_hash,point_id,state)
                SELECT repo,?,id,content_hash,?,'pending' FROM chunks
                WHERE repo=? AND id=? AND content_hash=?
                ON CONFLICT(repo,space_id,chunk_id,content_hash) DO UPDATE SET
                    state=CASE WHEN index_entries.state='delete' THEN 'pending' ELSE index_entries.state END`,
				space.ID(), domain.PointID(chunk), repo, chunk.ID, chunk.ContentHash)
			if err != nil {
				return fmt.Errorf("track catalog passage: %w", err)
			}
			if err := requireOne(result, "cannot track a passage whose source changed or disappeared"); err != nil {
				return err
			}
		}
		return nil
	})
}

// ConfirmChunks accepts only tracked current versions, after the caller has
// observed remote completion. Confirmation cannot resurrect a deletion tombstone.
func (s *Store) ConfirmChunks(ctx context.Context, repo string, space domain.EmbeddingSpace, chunks []domain.SearchChunk) error {
	if err := validateScope(repo, space); err != nil {
		return err
	}
	if err := validateMutationBatch(repo, chunks); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		for _, chunk := range chunks {
			result, err := tx.ExecContext(ctx, `UPDATE index_entries SET state='indexed'
                WHERE repo=? AND space_id=? AND chunk_id=? AND content_hash=? AND point_id=?
                AND state IN ('pending','indexed') AND EXISTS (
                    SELECT 1 FROM chunks c WHERE c.repo=index_entries.repo AND c.id=index_entries.chunk_id
                    AND c.content_hash=index_entries.content_hash
                )`, repo, space.ID(), chunk.ID, chunk.ContentHash, domain.PointID(chunk))
			if err != nil {
				return fmt.Errorf("confirm catalog passage: %w", err)
			}
			if err := requireOne(result, "cannot confirm an untracked, deleted, or stale passage version"); err != nil {
				return err
			}
		}
		return nil
	})
}

func requireOne(result sql.Result, message string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read catalog mutation count: %w", err)
	}
	if count != 1 {
		return invalid(message)
	}
	return nil
}

// MarkMissing invalidates only current indexed receipts. A stale observation or
// repeated missing-point report cannot change a newer receipt or deletion state.
func (s *Store) MarkMissing(ctx context.Context, repo string, space domain.EmbeddingSpace, chunks []domain.SearchChunk) error {
	if err := validateScope(repo, space); err != nil {
		return err
	}
	if err := validateMutationBatch(repo, chunks); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		for _, chunk := range chunks {
			if _, err := tx.ExecContext(ctx, `UPDATE index_entries SET state='pending'
                WHERE repo=? AND space_id=? AND chunk_id=? AND content_hash=? AND point_id=?
                AND state='indexed' AND EXISTS (
                    SELECT 1 FROM chunks c WHERE c.repo=index_entries.repo AND c.id=index_entries.chunk_id
                    AND c.content_hash=index_entries.content_hash
                )`, repo, space.ID(), chunk.ID, chunk.ContentHash, domain.PointID(chunk)); err != nil {
				return fmt.Errorf("mark missing catalog point: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) PendingDeletions(ctx context.Context, repo string, limit int) ([]domain.VectorDeletion, error) {
	if err := validatePage(repo, limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.repo,s.provider,s.model,s.revision,s.dimensions,e.point_id
        FROM index_entries e JOIN embedding_spaces s ON s.id=e.space_id
        WHERE e.repo=? AND e.state='delete' ORDER BY e.space_id,e.point_id LIMIT ?`, repo, limit)
	if err != nil {
		return nil, fmt.Errorf("list catalog pending deletions: %w", err)
	}
	return readDeletions(rows)
}

func readDeletions(rows *sql.Rows) (out []domain.VectorDeletion, retErr error) {
	defer closeRows(rows, &retErr)
	out = []domain.VectorDeletion{}
	for rows.Next() {
		var deletion domain.VectorDeletion
		if err := rows.Scan(&deletion.Repository, &deletion.Space.Provider, &deletion.Space.Model,
			&deletion.Space.Revision, &deletion.Space.Dimensions, &deletion.PointID); err != nil {
			return nil, fmt.Errorf("read catalog pending deletion: %w", err)
		}
		if err := validateDeletion(deletion); err != nil {
			return nil, fmt.Errorf("invalid stored catalog deletion: %w", err)
		}
		out = append(out, deletion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read catalog pending deletions: %w", err)
	}
	return out, nil
}

func validateDeletion(deletion domain.VectorDeletion) error {
	if err := validateScope(deletion.Repository, deletion.Space); err != nil {
		return err
	}
	id := deletion.PointID
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || strings.ToLower(id) != id {
		return invalid("catalog deletion point ID must be a canonical UUID")
	}
	compact := strings.ReplaceAll(id, "-", "")
	if len(compact) != 32 {
		return invalid("catalog deletion point ID must be a canonical UUID")
	}
	if _, err := hex.DecodeString(compact); err != nil {
		return invalid("catalog deletion point ID must be a canonical UUID")
	}
	return nil
}

func (s *Store) CompleteDeletions(ctx context.Context, deletions []domain.VectorDeletion) error {
	if err := validateMutationSize(len(deletions)); err != nil {
		return err
	}
	for _, deletion := range deletions {
		if err := validateDeletion(deletion); err != nil {
			return err
		}
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		for _, deletion := range deletions {
			if _, err := tx.ExecContext(ctx, `DELETE FROM index_entries
                WHERE repo=? AND space_id=? AND point_id=? AND state='delete'`,
				deletion.Repository, deletion.Space.ID(), deletion.PointID); err != nil {
				return fmt.Errorf("complete catalog point deletion: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) IndexStatus(ctx context.Context, repo string) (domain.IndexStatus, error) {
	status := domain.IndexStatus{Repository: repo, Spaces: []domain.EmbeddingSpaceStatus{}}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return status, invalid(err.Error())
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks WHERE repo=?", repo).Scan(&status.Chunks); err != nil {
			return fmt.Errorf("count catalog passages: %w", err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM index_entries WHERE repo=? AND state='delete'", repo).Scan(&status.PendingDeletes); err != nil {
			return fmt.Errorf("count catalog pending deletions: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT s.provider,s.model,s.revision,s.dimensions,
            (SELECT COUNT(*) FROM index_entries e JOIN chunks c
                ON c.repo=e.repo AND c.id=e.chunk_id AND c.content_hash=e.content_hash
                WHERE e.repo=? AND e.space_id=s.id AND e.state='indexed')
            FROM embedding_spaces s ORDER BY s.provider,s.model,s.revision,s.dimensions`, repo)
		if err != nil {
			return fmt.Errorf("read catalog embedding status: %w", err)
		}
		status.Spaces, err = readSpaceStatus(rows, status.Chunks)
		return err
	})
	if err != nil {
		return domain.IndexStatus{}, err
	}
	return status, nil
}

func readSpaceStatus(rows *sql.Rows, chunks int) (out []domain.EmbeddingSpaceStatus, retErr error) {
	defer closeRows(rows, &retErr)
	out = []domain.EmbeddingSpaceStatus{}
	for rows.Next() {
		var entry domain.EmbeddingSpaceStatus
		if err := rows.Scan(&entry.Space.Provider, &entry.Space.Model, &entry.Space.Revision,
			&entry.Space.Dimensions, &entry.EmbeddedChunks); err != nil {
			return nil, fmt.Errorf("read catalog embedding status: %w", err)
		}
		if err := entry.Space.Validate(); err != nil {
			return nil, fmt.Errorf("invalid stored catalog embedding space: %w", err)
		}
		entry.PendingChunks = chunks - entry.EmbeddedChunks
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read catalog embedding statuses: %w", err)
	}
	return out, nil
}
