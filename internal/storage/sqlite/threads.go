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
	(SELECT COUNT(DISTINCT r.item_number) FROM relationships r WHERE r.thread_id = t.id AND r.repo = t.repo) AS item_count,
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

	var existing int
	if qErr := s.db.QueryRowContext(ctx, "SELECT 1 FROM threads WHERE id = ?", string(thread.ID)).Scan(&existing); qErr == nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"threads.create",
			fmt.Sprintf("Thread %q already exists.", thread.ID),
			"Use a unique thread ID or call UpdateThread to modify an existing thread.",
			nil,
		)
	} else if !errors.Is(qErr, sql.ErrNoRows) {
		return fmt.Errorf("check existing thread %q: %w", thread.ID, qErr)
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
		WHERE id = ? AND repo = ?`,
		thread.Title,
		thread.Summary,
		string(thread.Status),
		thread.Revision,
		formatTime(thread.UpdatedAt),
		string(thread.ID),
		thread.Repository,
	)
	if err != nil {
		return fmt.Errorf("update thread %q: %w", thread.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected updating thread %q: %w", thread.ID, err)
	}
	if affected == 0 {
		var existingRepo string
		qErr := s.db.QueryRowContext(ctx, "SELECT repo FROM threads WHERE id = ?", string(thread.ID)).Scan(&existingRepo)
		if errors.Is(qErr, sql.ErrNoRows) {
			return domain.NewError(
				domain.ErrorCodeNotFound,
				"threads.update",
				fmt.Sprintf("Thread %q was not found.", thread.ID),
				"Run 'automain threads list --repo <owner/name>' to inspect existing thread IDs.",
				qErr,
			)
		}
		if qErr != nil {
			return fmt.Errorf("lookup thread %q repository: %w", thread.ID, qErr)
		}
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"threads.update",
			fmt.Sprintf("Thread %q belongs to repository %q, not %q.", thread.ID, existingRepo, thread.Repository),
			"Ensure the thread repository matches its stored repository.",
			nil,
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
	seenThreadIDs := make(map[domain.ThreadID]struct{}, len(record.ThreadsToCreate)+len(record.ThreadsToUpdate))
	for i := range record.ThreadsToCreate {
		if strings.TrimSpace(record.ThreadsToCreate[i].Repository) == "" {
			record.ThreadsToCreate[i].Repository = record.Repository
		}
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
		if record.ThreadsToCreate[i].Repository != record.Repository {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("New thread %q repository %q does not match analyzed item repository %q.", record.ThreadsToCreate[i].ID, record.ThreadsToCreate[i].Repository, record.Repository),
				"Ensure all created threads belong to the analyzed item's repository.",
				nil,
			)
		}
		if _, exists := seenThreadIDs[record.ThreadsToCreate[i].ID]; exists {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("Duplicate thread %q in analysis record for %s#%d.", record.ThreadsToCreate[i].ID, record.Repository, record.ItemNumber),
				"Ensure each thread ID appears at most once per item analysis record.",
				nil,
			)
		}
		seenThreadIDs[record.ThreadsToCreate[i].ID] = struct{}{}
	}
	for i := range record.ThreadsToUpdate {
		if strings.TrimSpace(record.ThreadsToUpdate[i].Repository) == "" {
			record.ThreadsToUpdate[i].Repository = record.Repository
		}
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
		if record.ThreadsToUpdate[i].Repository != record.Repository {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("Updated thread %q repository %q does not match analyzed item repository %q.", record.ThreadsToUpdate[i].ID, record.ThreadsToUpdate[i].Repository, record.Repository),
				"Ensure all updated threads belong to the analyzed item's repository.",
				nil,
			)
		}
		if _, exists := seenThreadIDs[record.ThreadsToUpdate[i].ID]; exists {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("Duplicate thread %q in analysis record for %s#%d.", record.ThreadsToUpdate[i].ID, record.Repository, record.ItemNumber),
				"Ensure each thread ID appears at most once per item analysis record.",
				nil,
			)
		}
		seenThreadIDs[record.ThreadsToUpdate[i].ID] = struct{}{}
	}
	for i := range record.Relationships {
		if strings.TrimSpace(record.Relationships[i].Repository) == "" {
			record.Relationships[i].Repository = record.Repository
		}
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
		if record.Relationships[i].Repository != record.Repository {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("Relationship %q repository %q does not match analyzed item repository %q.", record.Relationships[i].ID, record.Relationships[i].Repository, record.Repository),
				"Ensure all relationships in an item analysis record belong to the analyzed item's repository.",
				nil,
			)
		}
		if record.Relationships[i].ItemNumber != record.ItemNumber {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("Relationship %q item_number %d does not match analyzed item %s#%d.", record.Relationships[i].ID, record.Relationships[i].ItemNumber, record.Repository, record.ItemNumber),
				"Ensure all relationships in an item analysis record belong to the analyzed item.",
				nil,
			)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save analysis transaction: %w", err)
	}
	defer rollbackTx(tx)

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

	for _, t := range record.ThreadsToCreate {
		var existing int
		if qErr := tx.QueryRowContext(ctx, "SELECT 1 FROM threads WHERE id = ?", string(t.ID)).Scan(&existing); qErr == nil {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("Thread %q to create already exists.", t.ID),
				"Use ThreadsToUpdate to modify existing threads or provide a unique ID for new threads.",
				nil,
			)
		} else if !errors.Is(qErr, sql.ErrNoRows) {
			return fmt.Errorf("check existing thread %q during analysis save: %w", t.ID, qErr)
		}
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
		uRes, err := tx.ExecContext(
			ctx,
			`UPDATE threads SET
				title = ?,
				summary = ?,
				status = ?,
				revision = ?,
				updated_at = ?
			WHERE id = ? AND repo = ?`,
			t.Title,
			t.Summary,
			string(t.Status),
			t.Revision,
			formatTime(t.UpdatedAt),
			string(t.ID),
			t.Repository,
		)
		if err != nil {
			return fmt.Errorf("update thread %q during analysis save: %w", t.ID, err)
		}
		uAffected, err := uRes.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected updating thread %q: %w", t.ID, err)
		}
		if uAffected == 0 {
			var existingRepo string
			qErr := tx.QueryRowContext(ctx, "SELECT repo FROM threads WHERE id = ?", string(t.ID)).Scan(&existingRepo)
			if errors.Is(qErr, sql.ErrNoRows) {
				return domain.NewError(
					domain.ErrorCodeNotFound,
					"storage.save_analysis",
					fmt.Sprintf("Thread %q to update was not found.", t.ID),
					"Verify candidate thread IDs exist in SQLite.",
					qErr,
				)
			}
			if qErr != nil {
				return fmt.Errorf("lookup thread %q repository during analysis save: %w", t.ID, qErr)
			}
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.save_analysis",
				fmt.Sprintf("Thread %q belongs to repository %q, not %q.", t.ID, existingRepo, t.Repository),
				"Ensure candidate threads belong to the analyzed item's repository.",
				nil,
			)
		}
	}

	type priorRel struct {
		id       string
		threadID string
		relType  string
		verified bool
	}
	var priorRels []priorRel
	verifiedByThread := make(map[string]bool)
	priorRows, err := tx.QueryContext(
		ctx,
		"SELECT id, thread_id, rel_type, verified_by_human FROM relationships WHERE repo = ? AND item_number = ?",
		record.Repository,
		record.ItemNumber,
	)
	if err != nil {
		return fmt.Errorf("query existing relationships for %s#%d: %w", record.Repository, record.ItemNumber, err)
	}
	for priorRows.Next() {
		var pr priorRel
		var verInt int
		if err := priorRows.Scan(&pr.id, &pr.threadID, &pr.relType, &verInt); err != nil {
			closeRows(priorRows)
			return fmt.Errorf("scan existing relationship for %s#%d: %w", record.Repository, record.ItemNumber, err)
		}
		pr.verified = verInt != 0
		if pr.verified {
			verifiedByThread[pr.threadID] = true
		}
		priorRels = append(priorRels, pr)
	}
	if err := priorRows.Err(); err != nil {
		closeRows(priorRows)
		return fmt.Errorf("iterate existing relationships for %s#%d: %w", record.Repository, record.ItemNumber, err)
	}
	closeRows(priorRows)

	keepTuples := make(map[string]struct{}, len(record.Relationships))
	keepThreads := make(map[string]struct{}, len(record.Relationships))
	for _, rel := range record.Relationships {
		if verifiedByThread[string(rel.ThreadID)] {
			rel.VerifiedByHuman = true
		}
		if err := upsertRelationshipTx(ctx, tx, rel); err != nil {
			return err
		}
		keepTuples[string(rel.ThreadID)+"\x00"+string(rel.Type)] = struct{}{}
		keepThreads[string(rel.ThreadID)] = struct{}{}
	}

	for _, pr := range priorRels {
		tupleKey := pr.threadID + "\x00" + pr.relType
		if _, kept := keepTuples[tupleKey]; kept {
			continue
		}
		_, threadStillLinked := keepThreads[pr.threadID]
		if !pr.verified || threadStillLinked {
			if _, err := tx.ExecContext(ctx, "DELETE FROM relationships WHERE id = ?", pr.id); err != nil {
				return fmt.Errorf("delete superseded relationship %q for %s#%d: %w", pr.id, record.Repository, record.ItemNumber, err)
			}
		}
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
