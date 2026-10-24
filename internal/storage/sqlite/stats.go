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
		RepeatedProblems:    []domain.ProblemThreadStat{},
		MultiThreadItems:    []domain.ItemSummary{},
		UnresolvedItems:     []domain.ItemSummary{},
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
	if err := s.populateAnalyticsSlices(ctx, repo, &stats); err != nil {
		return domain.RepoStats{}, err
	}

	return stats, nil
}

// GetGraph returns the repository's threads, items with thread counts, and relationships.
func (s *Store) GetGraph(ctx context.Context, repo string) (domain.GraphView, error) {
	if err := ctx.Err(); err != nil {
		return domain.GraphView{}, domain.AsError("graph", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return domain.GraphView{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"graph",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}

	threads, err := s.ListThreads(ctx, domain.ThreadFilter{Repository: repo})
	if err != nil {
		return domain.GraphView{}, err
	}
	if threads == nil {
		threads = []domain.Thread{}
	}

	rels, err := s.ListRelationships(ctx, domain.RelationshipFilter{Repository: repo})
	if err != nil {
		return domain.GraphView{}, err
	}
	if rels == nil {
		rels = []domain.Relationship{}
	}

	itemRows, err := s.db.QueryContext(
		ctx,
		`SELECT
			i.repo,
			i.number,
			i.kind,
			i.state,
			i.title,
			COALESCE(i.author, ''),
			COALESCE(i.url, ''),
			(SELECT COUNT(DISTINCT r.thread_id) FROM relationships r WHERE r.repo = i.repo AND r.item_number = i.number) AS thread_count
		FROM items i
		WHERE i.repo = ?
		ORDER BY i.number ASC`,
		repo,
	)
	if err != nil {
		return domain.GraphView{}, fmt.Errorf("query graph items for %q: %w", repo, err)
	}
	defer closeRows(itemRows)

	items := []domain.ItemSummary{}
	for itemRows.Next() {
		var (
			it       domain.ItemSummary
			kindStr  string
			stateStr string
		)
		if err := itemRows.Scan(
			&it.Repository,
			&it.Number,
			&kindStr,
			&stateStr,
			&it.Title,
			&it.Author,
			&it.URL,
			&it.ThreadCount,
		); err != nil {
			return domain.GraphView{}, fmt.Errorf("scan graph item for %q: %w", repo, err)
		}
		it.Kind = domain.ItemKind(kindStr)
		it.State = domain.ItemState(stateStr)
		items = append(items, it)
	}
	if err := itemRows.Err(); err != nil {
		return domain.GraphView{}, fmt.Errorf("iterate graph items for %q: %w", repo, err)
	}

	return domain.GraphView{
		Repository:    repo,
		Threads:       threads,
		Items:         items,
		Relationships: rels,
	}, nil
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

func (s *Store) populateAnalyticsSlices(ctx context.Context, repo string, stats *domain.RepoStats) error {
	problemRows, err := s.db.QueryContext(
		ctx,
		`SELECT
			t.id,
			t.title,
			t.status,
			COUNT(DISTINCT CASE WHEN r.rel_type IN ('reports_same_problem', 'duplicate_of') THEN r.item_number END) AS problem_count,
			COUNT(DISTINCT r.item_number) AS total_items
		FROM threads t
		JOIN relationships r ON r.repo = t.repo AND r.thread_id = t.id
		WHERE t.repo = ?
		GROUP BY t.id, t.title, t.status
		HAVING problem_count > 0
		ORDER BY problem_count DESC, total_items DESC, t.id ASC
		LIMIT 10`,
		repo,
	)
	if err != nil {
		return fmt.Errorf("query repeated problems for %q: %w", repo, err)
	}
	for problemRows.Next() {
		var (
			p         domain.ProblemThreadStat
			idStr     string
			statusStr string
		)
		if err := problemRows.Scan(&idStr, &p.Title, &statusStr, &p.ProblemReportCount, &p.TotalItemCount); err != nil {
			closeRows(problemRows)
			return fmt.Errorf("scan repeated problem for %q: %w", repo, err)
		}
		p.ThreadID = domain.ThreadID(idStr)
		p.Status = domain.ThreadStatus(statusStr)
		stats.RepeatedProblems = append(stats.RepeatedProblems, p)
	}
	if err := problemRows.Err(); err != nil {
		closeRows(problemRows)
		return fmt.Errorf("iterate repeated problems for %q: %w", repo, err)
	}
	closeRows(problemRows)

	multiRows, err := s.db.QueryContext(
		ctx,
		`SELECT
			i.repo,
			i.number,
			i.kind,
			i.state,
			i.title,
			COALESCE(i.author, ''),
			COALESCE(i.url, ''),
			COUNT(DISTINCT r.thread_id) AS thread_count
		FROM items i
		JOIN relationships r ON r.repo = i.repo AND r.item_number = i.number
		WHERE i.repo = ?
		GROUP BY i.repo, i.number, i.kind, i.state, i.title, i.author, i.url
		HAVING COUNT(DISTINCT r.thread_id) > 1
		ORDER BY thread_count DESC, i.number ASC`,
		repo,
	)
	if err != nil {
		return fmt.Errorf("query multi-thread items for %q: %w", repo, err)
	}
	for multiRows.Next() {
		var (
			it       domain.ItemSummary
			kindStr  string
			stateStr string
		)
		if err := multiRows.Scan(&it.Repository, &it.Number, &kindStr, &stateStr, &it.Title, &it.Author, &it.URL, &it.ThreadCount); err != nil {
			closeRows(multiRows)
			return fmt.Errorf("scan multi-thread item for %q: %w", repo, err)
		}
		it.Kind = domain.ItemKind(kindStr)
		it.State = domain.ItemState(stateStr)
		stats.MultiThreadItems = append(stats.MultiThreadItems, it)
	}
	if err := multiRows.Err(); err != nil {
		closeRows(multiRows)
		return fmt.Errorf("iterate multi-thread items for %q: %w", repo, err)
	}
	closeRows(multiRows)

	unresRows, err := s.db.QueryContext(
		ctx,
		`SELECT
			i.repo,
			i.number,
			i.kind,
			i.state,
			i.title,
			COALESCE(i.author, ''),
			COALESCE(i.url, ''),
			(SELECT COUNT(DISTINCT r2.thread_id) FROM relationships r2 WHERE r2.repo = i.repo AND r2.item_number = i.number) AS thread_count
		FROM items i
		WHERE i.repo = ?
		  AND i.kind IN ('issue', 'discussion')
		  AND i.state = 'open'
		  AND EXISTS (
		      SELECT 1
		      FROM relationships r
		      JOIN threads t ON t.repo = r.repo AND t.id = r.thread_id
		      WHERE r.repo = i.repo
		        AND r.item_number = i.number
		        AND t.status = 'active'
		        AND NOT EXISTS (
		            SELECT 1
		            FROM relationships r_fix
		            JOIN items i_fix ON i_fix.repo = r_fix.repo AND i_fix.number = r_fix.item_number
		            WHERE r_fix.repo = t.repo
		              AND r_fix.thread_id = t.id
		              AND r_fix.rel_type IN ('proposes_fix', 'implements_decision')
		              AND i_fix.kind = 'pull_request'
		        )
		  )
		ORDER BY i.number ASC`,
		repo,
	)
	if err != nil {
		return fmt.Errorf("query unresolved items for %q: %w", repo, err)
	}
	for unresRows.Next() {
		var (
			it       domain.ItemSummary
			kindStr  string
			stateStr string
		)
		if err := unresRows.Scan(&it.Repository, &it.Number, &kindStr, &stateStr, &it.Title, &it.Author, &it.URL, &it.ThreadCount); err != nil {
			closeRows(unresRows)
			return fmt.Errorf("scan unresolved item for %q: %w", repo, err)
		}
		it.Kind = domain.ItemKind(kindStr)
		it.State = domain.ItemState(stateStr)
		stats.UnresolvedItems = append(stats.UnresolvedItems, it)
	}
	if err := unresRows.Err(); err != nil {
		closeRows(unresRows)
		return fmt.Errorf("iterate unresolved items for %q: %w", repo, err)
	}
	closeRows(unresRows)

	return nil
}
