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

const itemSelectColumns = `
	repo,
	number,
	COALESCE(external_id, ''),
	kind,
	state,
	title,
	COALESCE(body, ''),
	COALESCE(author, ''),
	COALESCE(labels_json, '[]'),
	COALESCE(url, ''),
	COALESCE(created_at, ''),
	COALESCE(updated_at, ''),
	content_hash,
	COALESCE(last_analyzed_hash, '')
`

// GetSyncWatermark returns the stored synchronization watermark and item/comment counts for repo.
func (s *Store) GetSyncWatermark(ctx context.Context, repo string) (domain.SyncWatermark, error) {
	if err := ctx.Err(); err != nil {
		return domain.SyncWatermark{}, domain.AsError("storage.watermark", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return domain.SyncWatermark{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"storage.watermark",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}

	wm := domain.SyncWatermark{
		Repository: repo,
	}

	var lastSyncedRaw, cursor string
	err := s.db.QueryRowContext(
		ctx,
		"SELECT COALESCE(last_synced_at, ''), COALESCE(cursor, '') FROM repositories WHERE repo = ?",
		repo,
	).Scan(&lastSyncedRaw, &cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.SyncWatermark{}, fmt.Errorf("query sync watermark for %q: %w", repo, err)
	}
	if err == nil {
		wm.Cursor = cursor
		if strings.TrimSpace(lastSyncedRaw) != "" {
			parsed, parseErr := parseTime(lastSyncedRaw)
			if parseErr != nil {
				return domain.SyncWatermark{}, fmt.Errorf("parse last_synced_at for %q: %w", repo, parseErr)
			}
			wm.LastSyncedAt = &parsed
		}
	}

	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items WHERE repo = ?", repo).Scan(&wm.ItemCount); err != nil {
		return domain.SyncWatermark{}, fmt.Errorf("count items for %q: %w", repo, err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments WHERE repo = ?", repo).Scan(&wm.CommentCount); err != nil {
		return domain.SyncWatermark{}, fmt.Errorf("count comments for %q: %w", repo, err)
	}

	return wm, nil
}

// UpsertItemsBatch persists a batch of items and comments and advances the repository sync watermark
// inside a single transaction. Unchanged items keep their existing last_analyzed_hash.
func (s *Store) UpsertItemsBatch(ctx context.Context, repo string, batch domain.FetchBatch, syncedAt time.Time) (domain.SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.SyncResult{}, domain.AsError("storage.upsert_batch", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return domain.SyncResult{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"storage.upsert_batch",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	} else {
		syncedAt = syncedAt.UTC()
	}

	normalizedItems := make([]domain.Item, len(batch.Items))
	seenItems := make(map[int]struct{}, len(batch.Items))
	for i, item := range batch.Items {
		if strings.TrimSpace(item.Repository) == "" {
			item.Repository = repo
		}
		if item.Repository != repo {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				fmt.Sprintf("Item #%d repository %q does not match target repository %q.", item.Number, item.Repository, repo),
				"Ensure all items in the sync batch belong to the target repository.",
				nil,
			)
		}
		if err := item.Validate(); err != nil {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				err.Error(),
				"Validate item fields before persisting to SQLite.",
				err,
			)
		}
		if _, exists := seenItems[item.Number]; exists {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				fmt.Sprintf("Duplicate item #%d in sync batch for %q.", item.Number, repo),
				"Deduplicate items by number before calling UpsertItemsBatch.",
				nil,
			)
		}
		seenItems[item.Number] = struct{}{}
		item.Labels = domain.NormalizeLabels(item.Labels)
		item.LastAnalyzedHash = ""
		normalizedItems[i] = item
	}

	normalizedComments := make([]domain.Comment, len(batch.Comments))
	seenComments := make(map[string]struct{}, len(batch.Comments))
	for i, c := range batch.Comments {
		if strings.TrimSpace(c.Repository) == "" {
			c.Repository = repo
		}
		if c.Repository != repo {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				fmt.Sprintf("Comment %q repository %q does not match target repository %q.", c.CommentID, c.Repository, repo),
				"Ensure all comments in the sync batch belong to the target repository.",
				nil,
			)
		}
		if c.ItemNumber <= 0 {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				fmt.Sprintf("Comment %q item_number must be positive, got %d.", c.CommentID, c.ItemNumber),
				"Ensure every comment references a positive GitHub item number.",
				nil,
			)
		}
		trimmedCommentID := strings.TrimSpace(c.CommentID)
		if trimmedCommentID == "" {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				fmt.Sprintf("Comment on item #%d is missing comment_id.", c.ItemNumber),
				"Ensure every comment has a non-empty comment_id.",
				nil,
			)
		}
		if _, exists := seenComments[trimmedCommentID]; exists {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				fmt.Sprintf("Duplicate comment %q in sync batch for %q.", trimmedCommentID, repo),
				"Deduplicate comments by comment_id before calling UpsertItemsBatch.",
				nil,
			)
		}
		seenComments[trimmedCommentID] = struct{}{}
		c.CommentID = trimmedCommentID
		normalizedComments[i] = c
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SyncResult{}, fmt.Errorf("begin upsert batch transaction: %w", err)
	}
	defer rollbackTx(tx)

	var newItems, updatedItems, unchangedItems int

	for _, item := range normalizedItems {
		labelsJSON, err := encodeLabels(item.Labels)
		if err != nil {
			return domain.SyncResult{}, err
		}

		var existingContentHash string
		qErr := tx.QueryRowContext(
			ctx,
			"SELECT content_hash FROM items WHERE repo = ? AND number = ?",
			repo,
			item.Number,
		).Scan(&existingContentHash)

		if errors.Is(qErr, sql.ErrNoRows) {
			newItems++
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO items (
					repo, number, external_id, kind, state, title, body, author,
					labels_json, url, created_at, updated_at, content_hash, last_analyzed_hash
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
				repo,
				item.Number,
				item.ExternalID,
				string(item.Kind),
				string(item.State),
				item.Title,
				item.Body,
				item.Author,
				labelsJSON,
				item.URL,
				formatTime(item.CreatedAt),
				formatTime(item.UpdatedAt),
				item.ContentHash,
			); err != nil {
				return domain.SyncResult{}, fmt.Errorf("insert item %s#%d: %w", repo, item.Number, err)
			}
			continue
		}
		if qErr != nil {
			return domain.SyncResult{}, fmt.Errorf("inspect existing item %s#%d: %w", repo, item.Number, qErr)
		}

		if existingContentHash == item.ContentHash {
			unchangedItems++
		} else {
			updatedItems++
		}

		if _, err := tx.ExecContext(
			ctx,
			`UPDATE items SET
				external_id = ?,
				kind = ?,
				state = ?,
				title = ?,
				body = ?,
				author = ?,
				labels_json = ?,
				url = ?,
				created_at = ?,
				updated_at = ?,
				content_hash = ?
			WHERE repo = ? AND number = ?`,
			item.ExternalID,
			string(item.Kind),
			string(item.State),
			item.Title,
			item.Body,
			item.Author,
			labelsJSON,
			item.URL,
			formatTime(item.CreatedAt),
			formatTime(item.UpdatedAt),
			item.ContentHash,
			repo,
			item.Number,
		); err != nil {
			return domain.SyncResult{}, fmt.Errorf("update item %s#%d: %w", repo, item.Number, err)
		}
	}

	for _, c := range normalizedComments {
		if _, inBatch := seenItems[c.ItemNumber]; !inBatch {
			var exists int
			qErr := tx.QueryRowContext(
				ctx,
				"SELECT 1 FROM items WHERE repo = ? AND number = ?",
				repo,
				c.ItemNumber,
			).Scan(&exists)
			if errors.Is(qErr, sql.ErrNoRows) {
				return domain.SyncResult{}, domain.NewError(
					domain.ErrorCodeNotFound,
					"storage.upsert_batch",
					fmt.Sprintf("Parent item %s#%d for comment %q was not found.", repo, c.ItemNumber, c.CommentID),
					"Include the parent item in the sync batch before upserting its comments.",
					qErr,
				)
			}
			if qErr != nil {
				return domain.SyncResult{}, fmt.Errorf("lookup parent item %s#%d for comment %q: %w", repo, c.ItemNumber, c.CommentID, qErr)
			}
		}
		var existingCommentItem int
		cErr := tx.QueryRowContext(
			ctx,
			"SELECT item_number FROM comments WHERE repo = ? AND comment_id = ?",
			repo,
			c.CommentID,
		).Scan(&existingCommentItem)
		if cErr != nil && !errors.Is(cErr, sql.ErrNoRows) {
			return domain.SyncResult{}, fmt.Errorf("lookup existing comment %q in %q: %w", c.CommentID, repo, cErr)
		}
		if cErr == nil && existingCommentItem != c.ItemNumber {
			return domain.SyncResult{}, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"storage.upsert_batch",
				fmt.Sprintf("Comment %q in %q already belongs to item #%d, not #%d.", c.CommentID, repo, existingCommentItem, c.ItemNumber),
				"Ensure comment IDs are unique across repository items.",
				nil,
			)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO comments (
				repo, comment_id, item_number, author, body, url, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (repo, comment_id) DO UPDATE SET
				item_number = excluded.item_number,
				author = excluded.author,
				body = excluded.body,
				url = excluded.url,
				created_at = excluded.created_at,
				updated_at = excluded.updated_at`,
			repo,
			c.CommentID,
			c.ItemNumber,
			c.Author,
			c.Body,
			c.URL,
			formatTime(c.CreatedAt),
			formatTime(c.UpdatedAt),
		); err != nil {
			return domain.SyncResult{}, fmt.Errorf("upsert comment %q on %s#%d: %w", c.CommentID, repo, c.ItemNumber, err)
		}
	}

	var dirtyCount int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM items
		 WHERE repo = ?
		   AND (last_analyzed_hash IS NULL OR last_analyzed_hash = '' OR content_hash != last_analyzed_hash)`,
		repo,
	).Scan(&dirtyCount); err != nil {
		return domain.SyncResult{}, fmt.Errorf("count dirty items for %q: %w", repo, err)
	}

	syncedAtStr := formatTime(syncedAt)
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO repositories (repo, last_synced_at, cursor, sync_status, updated_at)
		 VALUES (?, ?, '', 'synced', ?)
		 ON CONFLICT (repo) DO UPDATE SET
		     last_synced_at = excluded.last_synced_at,
		     sync_status = excluded.sync_status,
		     updated_at = excluded.updated_at`,
		repo,
		syncedAtStr,
		syncedAtStr,
	); err != nil {
		return domain.SyncResult{}, fmt.Errorf("update sync watermark for %q: %w", repo, err)
	}

	if err := tx.Commit(); err != nil {
		return domain.SyncResult{}, fmt.Errorf("commit upsert batch for %q: %w", repo, err)
	}

	return domain.SyncResult{
		Repository:                repo,
		SyncedAt:                  syncedAt,
		FetchedItems:              len(normalizedItems),
		FetchedComments:           len(normalizedComments),
		NewItems:                  newItems,
		UpdatedItems:              updatedItems,
		UnchangedItems:            unchangedItems,
		DirtyItemsPendingAnalysis: dirtyCount,
		DiscussionsSynced:         batch.DiscussionsSynced,
		Warnings:                  batch.Warnings,
	}, nil
}

// GetItem fetches a single repository item by number.
func (s *Store) GetItem(ctx context.Context, repo string, number int) (domain.Item, error) {
	if err := ctx.Err(); err != nil {
		return domain.Item{}, domain.AsError("items.show", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return domain.Item{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items.show",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}
	if number <= 0 {
		return domain.Item{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items.show",
			fmt.Sprintf("Item number must be a positive integer, got %d.", number),
			"Pass a positive GitHub item number.",
			nil,
		)
	}

	query := "SELECT " + itemSelectColumns + " FROM items WHERE repo = ? AND number = ?"
	item, err := scanItem(s.db.QueryRowContext(ctx, query, repo, number))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Item{}, domain.NewError(
			domain.ErrorCodeNotFound,
			"items.show",
			fmt.Sprintf("Item %s#%d was not found.", repo, number),
			fmt.Sprintf("Run 'automain sync --repo %s' to import repository items.", repo),
			err,
		)
	}
	if err != nil {
		return domain.Item{}, fmt.Errorf("query item %s#%d: %w", repo, number, err)
	}
	return item, nil
}

// GetItemWithComments fetches an item together with all stored comments ordered chronologically.
func (s *Store) GetItemWithComments(ctx context.Context, repo string, number int) (domain.Item, []domain.Comment, error) {
	item, err := s.GetItem(ctx, repo, number)
	if err != nil {
		return domain.Item{}, nil, err
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT
			repo,
			item_number,
			comment_id,
			COALESCE(author, ''),
			COALESCE(body, ''),
			COALESCE(url, ''),
			COALESCE(created_at, ''),
			COALESCE(updated_at, '')
		FROM comments
		WHERE repo = ? AND item_number = ?
		ORDER BY created_at ASC, comment_id ASC`,
		repo,
		number,
	)
	if err != nil {
		return domain.Item{}, nil, fmt.Errorf("query comments for %s#%d: %w", repo, number, err)
	}
	defer closeRows(rows)

	comments := []domain.Comment{}
	for rows.Next() {
		var c domain.Comment
		var createdRaw, updatedRaw string
		if err := rows.Scan(
			&c.Repository,
			&c.ItemNumber,
			&c.CommentID,
			&c.Author,
			&c.Body,
			&c.URL,
			&createdRaw,
			&updatedRaw,
		); err != nil {
			return domain.Item{}, nil, fmt.Errorf("scan comment for %s#%d: %w", repo, number, err)
		}
		createdAt, err := parseTime(createdRaw)
		if err != nil {
			return domain.Item{}, nil, fmt.Errorf("parse comment %q created_at: %w", c.CommentID, err)
		}
		updatedAt, err := parseTime(updatedRaw)
		if err != nil {
			return domain.Item{}, nil, fmt.Errorf("parse comment %q updated_at: %w", c.CommentID, err)
		}
		c.CreatedAt = createdAt
		c.UpdatedAt = updatedAt
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return domain.Item{}, nil, fmt.Errorf("iterate comments for %s#%d: %w", repo, number, err)
	}

	return item, comments, nil
}

// ListItems returns repository items matching filter ordered by item number ascending.
func (s *Store) ListItems(ctx context.Context, filter domain.ItemFilter) ([]domain.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("items.list", err)
	}
	if err := domain.ValidateRepoSlug(filter.Repository); err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items.list",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}
	if filter.Kind != "" {
		if err := filter.Kind.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.list",
				err.Error(),
				"Use kind issue, pull_request, or discussion.",
				err,
			)
		}
	}
	if filter.State != "" {
		if err := filter.State.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.list",
				err.Error(),
				"Use state open, closed, merged, or answered.",
				err,
			)
		}
	}
	if filter.Limit < 0 {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items.list",
			"Item limit must not be negative.",
			"Pass a positive integer limit or 0 for all matching items.",
			nil,
		)
	}

	var query strings.Builder
	query.WriteString("SELECT ")
	query.WriteString(itemSelectColumns)
	query.WriteString(" FROM items WHERE repo = ?")
	args := []any{filter.Repository}

	if filter.Kind != "" {
		query.WriteString(" AND kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.State != "" {
		query.WriteString(" AND state = ?")
		args = append(args, string(filter.State))
	}
	if filter.DirtyOnly {
		query.WriteString(" AND (last_analyzed_hash IS NULL OR last_analyzed_hash = '' OR content_hash != last_analyzed_hash)")
	}
	query.WriteString(" ORDER BY number ASC")
	if filter.Limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list items for %q: %w", filter.Repository, err)
	}
	defer closeRows(rows)

	items := []domain.Item{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan item for %q: %w", filter.Repository, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate items for %q: %w", filter.Repository, err)
	}

	return items, nil
}

// ListDirtyItems returns items in repo whose content_hash differs from last_analyzed_hash.
func (s *Store) ListDirtyItems(ctx context.Context, repo string, limit int) ([]domain.Item, error) {
	return s.ListItems(ctx, domain.ItemFilter{
		Repository: repo,
		DirtyOnly:  true,
		Limit:      limit,
	})
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(scanner rowScanner) (domain.Item, error) {
	var (
		item       domain.Item
		kindStr    string
		stateStr   string
		labelsRaw  string
		createdRaw string
		updatedRaw string
	)
	if err := scanner.Scan(
		&item.Repository,
		&item.Number,
		&item.ExternalID,
		&kindStr,
		&stateStr,
		&item.Title,
		&item.Body,
		&item.Author,
		&labelsRaw,
		&item.URL,
		&createdRaw,
		&updatedRaw,
		&item.ContentHash,
		&item.LastAnalyzedHash,
	); err != nil {
		return domain.Item{}, err
	}

	item.Kind = domain.ItemKind(kindStr)
	item.State = domain.ItemState(stateStr)

	labels, err := decodeLabels(labelsRaw)
	if err != nil {
		return domain.Item{}, err
	}
	item.Labels = labels

	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return domain.Item{}, fmt.Errorf("parse item %s#%d created_at: %w", item.Repository, item.Number, err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return domain.Item{}, fmt.Errorf("parse item %s#%d updated_at: %w", item.Repository, item.Number, err)
	}
	item.CreatedAt = createdAt
	item.UpdatedAt = updatedAt

	return item, nil
}
