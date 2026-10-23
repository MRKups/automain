package sqlite

import (
	"context"
	"fmt"

	"github.com/MRKups/automain/internal/domain"
)

// GetRepoStats computes repository item, thread, relationship, and dirty-state statistics from SQLite.
func (s *Store) GetRepoStats(ctx context.Context, repo string) (domain.RepoStats, error) {
	if err := ctx.Err(); err != nil {
		return domain.RepoStats{}, domain.AsError("stats", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return domain.RepoStats{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"stats",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}

	stats := domain.RepoStats{
		Repository:          repo,
		ItemsByKind:         map[domain.ItemKind]int{},
		ItemsByState:        map[domain.ItemState]int{},
		ThreadsByStatus:     map[domain.ThreadStatus]int{},
		RelationshipsByType: map[domain.RelationshipType]int{},
	}

	if err := s.populateItemStats(ctx, repo, &stats); err != nil {
		return domain.RepoStats{}, err
	}
	if err := s.populateThreadStats(ctx, repo, &stats); err != nil {
		return domain.RepoStats{}, err
	}
	if err := s.populateRelationshipStats(ctx, repo, &stats); err != nil {
		return domain.RepoStats{}, err
	}

	return stats, nil
}

func (s *Store) populateItemStats(ctx context.Context, repo string, stats *domain.RepoStats) error {
	kindRows, err := s.db.QueryContext(
		ctx,
		"SELECT kind, COUNT(*) FROM items WHERE repo = ? GROUP BY kind",
		repo,
	)
	if err != nil {
		return fmt.Errorf("count items by kind for %q: %w", repo, err)
	}
	for kindRows.Next() {
		var kindStr string
		var count int
		if err := kindRows.Scan(&kindStr, &count); err != nil {
			closeRows(kindRows)
			return fmt.Errorf("scan item kind count for %q: %w", repo, err)
		}
		stats.ItemsByKind[domain.ItemKind(kindStr)] = count
		stats.TotalItems += count
	}
	if err := kindRows.Err(); err != nil {
		closeRows(kindRows)
		return fmt.Errorf("iterate item kind counts for %q: %w", repo, err)
	}
	closeRows(kindRows)

	stateRows, err := s.db.QueryContext(
		ctx,
		"SELECT state, COUNT(*) FROM items WHERE repo = ? GROUP BY state",
		repo,
	)
	if err != nil {
		return fmt.Errorf("count items by state for %q: %w", repo, err)
	}
	for stateRows.Next() {
		var stateStr string
		var count int
		if err := stateRows.Scan(&stateStr, &count); err != nil {
			closeRows(stateRows)
			return fmt.Errorf("scan item state count for %q: %w", repo, err)
		}
		stats.ItemsByState[domain.ItemState(stateStr)] = count
	}
	if err := stateRows.Err(); err != nil {
		closeRows(stateRows)
		return fmt.Errorf("iterate item state counts for %q: %w", repo, err)
	}
	closeRows(stateRows)

	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM items
		 WHERE repo = ?
		   AND (last_analyzed_hash IS NULL OR last_analyzed_hash = '' OR content_hash != last_analyzed_hash)`,
		repo,
	).Scan(&stats.DirtyItemsCount); err != nil {
		return fmt.Errorf("count dirty items for %q: %w", repo, err)
	}

	return nil
}

func (s *Store) populateThreadStats(ctx context.Context, repo string, stats *domain.RepoStats) error {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT status, COUNT(*) FROM threads WHERE repo = ? GROUP BY status",
		repo,
	)
	if err != nil {
		return fmt.Errorf("count threads by status for %q: %w", repo, err)
	}
	defer closeRows(rows)

	for rows.Next() {
		var statusStr string
		var count int
		if err := rows.Scan(&statusStr, &count); err != nil {
			return fmt.Errorf("scan thread status count for %q: %w", repo, err)
		}
		stats.ThreadsByStatus[domain.ThreadStatus(statusStr)] = count
		stats.TotalThreads += count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate thread status counts for %q: %w", repo, err)
	}

	return nil
}

func (s *Store) populateRelationshipStats(ctx context.Context, repo string, stats *domain.RepoStats) error {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT rel_type, COUNT(*) FROM relationships WHERE repo = ? GROUP BY rel_type",
		repo,
	)
	if err != nil {
		return fmt.Errorf("count relationships by type for %q: %w", repo, err)
	}
	for rows.Next() {
		var typeStr string
		var count int
		if err := rows.Scan(&typeStr, &count); err != nil {
			closeRows(rows)
			return fmt.Errorf("scan relationship type count for %q: %w", repo, err)
		}
		stats.RelationshipsByType[domain.RelationshipType(typeStr)] = count
		stats.TotalRelationships += count
	}
	if err := rows.Err(); err != nil {
		closeRows(rows)
		return fmt.Errorf("iterate relationship type counts for %q: %w", repo, err)
	}
	closeRows(rows)

	if err := s.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM relationships WHERE repo = ? AND verified_by_human != 0",
		repo,
	).Scan(&stats.VerifiedRelationshipsCount); err != nil {
		return fmt.Errorf("count verified relationships for %q: %w", repo, err)
	}

	if err := s.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM relationships WHERE repo = ? AND explicitly_linked = 0",
		repo,
	).Scan(&stats.NonObviousRelationshipsCount); err != nil {
		return fmt.Errorf("count non-obvious relationships for %q: %w", repo, err)
	}

	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM (
			SELECT item_number
			FROM relationships
			WHERE repo = ?
			GROUP BY item_number
			HAVING COUNT(DISTINCT thread_id) > 1
		)`,
		repo,
	).Scan(&stats.MultiThreadItemsCount); err != nil {
		return fmt.Errorf("count multi-thread items for %q: %w", repo, err)
	}

	return nil
}
