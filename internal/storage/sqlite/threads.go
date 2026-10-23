package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

const threadSelectColumns = `
	t.id,
	t.repo,
	t.title,
	t.summary,
	t.status,
	(SELECT COUNT(DISTINCT r.item_number) FROM relationships r WHERE r.thread_id = t.id) AS item_count,
	t.revision,
	COALESCE(t.created_at, ''),
	COALESCE(t.updated_at, '')
`

// CreateThread inserts a new persistent thread into SQLite.
func (s *Store) CreateThread(ctx context.Context, thread domain.Thread) error {
	if err := ctx.Err(); err != nil {
		return domain.AsError("threads.create", err)
	}
	if thread.Revision <= 0 {
		thread.Revision = 1
	}
	if err := thread.Validate(); err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"threads.create",
			err.Error(),
			"Ensure thread id, repository, title, summary, status, and revision are valid.",
			err,
		)
	}
	now := time.Now().UTC()
	if thread.CreatedAt.IsZero() {
		thread.CreatedAt = now
	}
	if thread.UpdatedAt.IsZero() {
		thread.UpdatedAt = thread.CreatedAt
	}

	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO threads (id, repo, title, summary, status, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(thread.ID),
		thread.Repository,
		thread.Title,
		thread.Summary,
		string(thread.Status),
		thread.Revision,
		formatTime(thread.CreatedAt),
		formatTime(thread.UpdatedAt),
	); err != nil {
		return fmt.Errorf("insert thread %q: %w", thread.ID, err)
	}
	return nil
}

// UpdateThread updates an existing thread's title, summary, status, revision, and updated_at timestamp.
func (s *Store) UpdateThread(ctx context.Context, thread domain.Thread) error {
	if err := ctx.Err(); err != nil {
		return domain.AsError("threads.update", err)
	}
	if err := thread.Validate(); err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"threads.update",
			err.Error(),
			"Ensure thread id, repository, title, summary, status, and revision are valid.",
			err,
		)
	}
	if thread.UpdatedAt.IsZero() {
		thread.UpdatedAt = time.Now().UTC()
	}

	res, err := s.db.ExecContext(
		ctx,
		`UPDATE threads SET
			title = ?,
			summary = ?,
			status = ?,
			revision = ?,
			updated_at = ?
		WHERE id = ?`,
		thread.Title,
		thread.Summary,
		string(thread.Status),
		thread.Revision,
		formatTime(thread.UpdatedAt),
		string(thread.ID),
	)
	if err != nil {
		return fmt.Errorf("update thread %q: %w", thread.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected updating thread %q: %w", thread.ID, err)
	}
	if affected == 0 {
		return domain.NewError(
			domain.ErrorCodeNotFound,
			"threads.update",
			fmt.Sprintf("Thread %q was not found.", thread.ID),
			"Run 'automain threads list --repo <owner/name>' to inspect existing thread IDs.",
			sql.ErrNoRows,
		)
	}
	return nil
}

// ListThreads returns persistent threads matching filter ordered by most recently updated first.
func (s *Store) ListThreads(ctx context.Context, filter domain.ThreadFilter) ([]domain.Thread, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("threads.list", err)
	}
	if strings.TrimSpace(filter.Repository) != "" {
		if err := domain.ValidateRepoSlug(filter.Repository); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"threads.list",
				err.Error(),
				"Pass the repository in owner/name format.",
				err,
			)
		}
	}
	if filter.Status != "" {
		if err := filter.Status.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"threads.list",
				err.Error(),
				"Use --status active, --status resolved, or --status stale.",
				err,
			)
		}
	}

	var query strings.Builder
	query.WriteString("SELECT ")
	query.WriteString(threadSelectColumns)
	query.WriteString(" FROM threads t WHERE 1=1")
	args := []any{}

	if strings.TrimSpace(filter.Repository) != "" {
		query.WriteString(" AND t.repo = ?")
		args = append(args, filter.Repository)
	}
	if filter.Status != "" {
		query.WriteString(" AND t.status = ?")
		args = append(args, string(filter.Status))
	}
	query.WriteString(" ORDER BY t.updated_at DESC, t.id ASC")

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	defer closeRows(rows)

	threads := []domain.Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, fmt.Errorf("scan thread: %w", err)
		}
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate threads: %w", err)
	}

	return threads, nil
}

// GetThreadDetail returns a single thread together with its linked items and relationships.
func (s *Store) GetThreadDetail(ctx context.Context, id domain.ThreadID) (domain.ThreadDetail, error) {
	if err := ctx.Err(); err != nil {
		return domain.ThreadDetail{}, domain.AsError("threads.show", err)
	}
	trimmedID := strings.TrimSpace(string(id))
	if trimmedID == "" {
		return domain.ThreadDetail{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"threads.show",
			"Thread ID (--id) is required.",
			"Pass a non-empty thread ID via --id <thread-id>.",
			nil,
		)
	}

	query := "SELECT " + threadSelectColumns + " FROM threads t WHERE t.id = ?"
	thread, err := scanThread(s.db.QueryRowContext(ctx, query, trimmedID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadDetail{}, domain.NewError(
			domain.ErrorCodeNotFound,
			"threads.show",
			fmt.Sprintf("Thread %q was not found.", trimmedID),
			"Run 'automain threads list --repo <owner/name>' to inspect available thread IDs.",
			err,
		)
	}
	if err != nil {
		return domain.ThreadDetail{}, fmt.Errorf("query thread %q: %w", trimmedID, err)
	}

	rels, err := s.ListRelationships(ctx, domain.RelationshipFilter{
		Repository: thread.Repository,
		ThreadID:   thread.ID,
	})
	if err != nil {
		return domain.ThreadDetail{}, err
	}

	itemRows, err := s.db.QueryContext(
		ctx,
		`SELECT
			i.repo,
			i.number,
			COALESCE(i.external_id, ''),
			i.kind,
			i.state,
			i.title,
			COALESCE(i.body, ''),
			COALESCE(i.author, ''),
			COALESCE(i.labels_json, '[]'),
			COALESCE(i.url, ''),
			COALESCE(i.created_at, ''),
			COALESCE(i.updated_at, ''),
			i.content_hash,
			COALESCE(i.last_analyzed_hash, '')
		FROM items i
		WHERE i.repo = ?
		  AND EXISTS (
		      SELECT 1 FROM relationships r
		      WHERE r.thread_id = ? AND r.repo = i.repo AND r.item_number = i.number
		  )
		ORDER BY i.number ASC`,
		thread.Repository,
		string(thread.ID),
	)
	if err != nil {
		return domain.ThreadDetail{}, fmt.Errorf("query member items for thread %q: %w", thread.ID, err)
	}
	defer closeRows(itemRows)

	items := []domain.Item{}
	for itemRows.Next() {
		item, err := scanItem(itemRows)
		if err != nil {
			return domain.ThreadDetail{}, fmt.Errorf("scan member item for thread %q: %w", thread.ID, err)
		}
		items = append(items, item)
	}
	if err := itemRows.Err(); err != nil {
		return domain.ThreadDetail{}, fmt.Errorf("iterate member items for thread %q: %w", thread.ID, err)
	}

	return domain.ThreadDetail{
		Thread:        thread,
		Items:         items,
		Relationships: rels,
	}, nil
}

// SaveItemAnalysis atomically creates new threads, updates matched threads, upserts relationships
// with evidence, and records the analyzed content hash on the source item.
func (s *Store) SaveItemAnalysis(ctx context.Context, record domain.ItemAnalysisRecord) error {
	if err := ctx.Err(); err != nil {
		return domain.AsError("storage.save_analysis", err)
	}
	if err := domain.ValidateRepoSlug(record.Repository); err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"storage.save_analysis",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}
	if record.ItemNumber <= 0 {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"storage.save_analysis",
			fmt.Sprintf("Item number must be positive, got %d.", record.ItemNumber),
			"Pass a valid GitHub item number.",
			nil,
		)
	}
	if strings.TrimSpace(record.AnalyzedHash) == "" {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"storage.save_analysis",
			fmt.Sprintf("Analyzed content hash for %s#%d must not be empty.", record.Repository, record.ItemNumber),
			"Compute the item content_hash before recording analysis results.",
			nil,
		)
	}

	now := time.Now().UTC()
	for i := range record.ThreadsToCreate {
		if record.ThreadsToCreate[i].Revision <= 0 {
			record.ThreadsToCreate[i].Revision = 1
		}
		if record.ThreadsToCreate[i].CreatedAt.IsZero() {
			record.ThreadsToCreate[i].CreatedAt = now
		}
		if record.ThreadsToCreate[i].UpdatedAt.IsZero() {
			record.ThreadsToCreate[i].UpdatedAt = record.ThreadsToCreate[i].CreatedAt
		}
		if err := record.ThreadsToCreate[i].Validate(); err != nil {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				err.Error(),
				"Validate new thread proposals before saving.",
				err,
			)
		}
	}
	for i := range record.ThreadsToUpdate {
		if record.ThreadsToUpdate[i].UpdatedAt.IsZero() {
			record.ThreadsToUpdate[i].UpdatedAt = now
		}
		if err := record.ThreadsToUpdate[i].Validate(); err != nil {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				err.Error(),
				"Validate updated threads before saving.",
				err,
			)
		}
	}
	for i := range record.Relationships {
		if record.Relationships[i].CreatedAt.IsZero() {
			record.Relationships[i].CreatedAt = now
		}
		if record.Relationships[i].UpdatedAt.IsZero() {
			record.Relationships[i].UpdatedAt = record.Relationships[i].CreatedAt
		}
		if err := record.Relationships[i].Validate(); err != nil {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				err.Error(),
				"Every relationship must have a valid type, non-empty explanation, and source evidence.",
				err,
			)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save analysis transaction: %w", err)
	}
	defer rollbackTx(tx)

	for _, t := range record.ThreadsToCreate {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO threads (id, repo, title, summary, status, revision, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			string(t.ID),
			t.Repository,
			t.Title,
			t.Summary,
			string(t.Status),
			t.Revision,
			formatTime(t.CreatedAt),
			formatTime(t.UpdatedAt),
		); err != nil {
			return fmt.Errorf("insert thread %q during analysis save: %w", t.ID, err)
		}
	}

	for _, t := range record.ThreadsToUpdate {
		res, err := tx.ExecContext(
			ctx,
			`UPDATE threads SET
				title = ?,
				summary = ?,
				status = ?,
				revision = ?,
				updated_at = ?
			WHERE id = ?`,
			t.Title,
			t.Summary,
			string(t.Status),
			t.Revision,
			formatTime(t.UpdatedAt),
			string(t.ID),
		)
		if err != nil {
			return fmt.Errorf("update thread %q during analysis save: %w", t.ID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected updating thread %q: %w", t.ID, err)
		}
		if affected == 0 {
			return domain.NewError(
				domain.ErrorCodeNotFound,
				"storage.save_analysis",
				fmt.Sprintf("Thread %q to update was not found.", t.ID),
				"Verify candidate thread IDs exist in SQLite.",
				sql.ErrNoRows,
			)
		}
	}

	for _, rel := range record.Relationships {
		if err := upsertRelationshipTx(ctx, tx, rel); err != nil {
			return err
		}
	}

	res, err := tx.ExecContext(
		ctx,
		`UPDATE items SET last_analyzed_hash = ? WHERE repo = ? AND number = ?`,
		record.AnalyzedHash,
		record.Repository,
		record.ItemNumber,
	)
	if err != nil {
		return fmt.Errorf("update last_analyzed_hash for %s#%d: %w", record.Repository, record.ItemNumber, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected updating %s#%d: %w", record.Repository, record.ItemNumber, err)
	}
	if affected == 0 {
		return domain.NewError(
			domain.ErrorCodeNotFound,
			"storage.save_analysis",
			fmt.Sprintf("Item %s#%d was not found in SQLite.", record.Repository, record.ItemNumber),
			fmt.Sprintf("Run 'automain sync --repo %s' before analyzing items.", record.Repository),
			sql.ErrNoRows,
		)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit analysis transaction for %s#%d: %w", record.Repository, record.ItemNumber, err)
	}
	return nil
}

func scanThread(scanner rowScanner) (domain.Thread, error) {
	var (
		t          domain.Thread
		idStr      string
		statusStr  string
		createdRaw string
		updatedRaw string
	)
	if err := scanner.Scan(
		&idStr,
		&t.Repository,
		&t.Title,
		&t.Summary,
		&statusStr,
		&t.ItemCount,
		&t.Revision,
		&createdRaw,
		&updatedRaw,
	); err != nil {
		return domain.Thread{}, err
	}

	t.ID = domain.ThreadID(idStr)
	t.Status = domain.ThreadStatus(statusStr)

	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return domain.Thread{}, fmt.Errorf("parse thread %q created_at: %w", t.ID, err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return domain.Thread{}, fmt.Errorf("parse thread %q updated_at: %w", t.ID, err)
	}
	t.CreatedAt = createdAt
	t.UpdatedAt = updatedAt

	return t, nil
}
