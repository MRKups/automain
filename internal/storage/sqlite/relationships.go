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

const relationshipSelectColumns = `
	id,
	repo,
	thread_id,
	item_number,
	rel_type,
	explanation,
	confidence,
	explicitly_linked,
	verified_by_human,
	COALESCE(created_at, ''),
	COALESCE(updated_at, '')
`

// UpsertRelationship validates and persists a relationship together with its source evidence references.
func (s *Store) UpsertRelationship(ctx context.Context, rel domain.Relationship) error {
	if err := ctx.Err(); err != nil {
		return domain.AsError("relationships.upsert", err)
	}
	now := time.Now().UTC()
	if rel.CreatedAt.IsZero() {
		rel.CreatedAt = now
	}
	if rel.UpdatedAt.IsZero() {
		rel.UpdatedAt = rel.CreatedAt
	}
	if err := rel.Validate(); err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.upsert",
			err.Error(),
			"Ensure the relationship has a valid type, non-empty explanation, and at least one source evidence reference.",
			err,
		)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert relationship transaction: %w", err)
	}
	defer rollbackTx(tx)

	if err := upsertRelationshipTx(ctx, tx, rel); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit relationship %q: %w", rel.ID, err)
	}
	return nil
}

func upsertRelationshipTx(ctx context.Context, tx *sql.Tx, rel domain.Relationship) error {
	var threadRepo string
	if err := tx.QueryRowContext(
		ctx,
		"SELECT repo FROM threads WHERE id = ?",
		string(rel.ThreadID),
	).Scan(&threadRepo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(
				domain.ErrorCodeNotFound,
				"relationships.upsert",
				fmt.Sprintf("Thread %q for relationship %q was not found.", rel.ThreadID, rel.ID),
				"Create the thread before linking relationships to it.",
				err,
			)
		}
		return fmt.Errorf("lookup thread %q for relationship %q: %w", rel.ThreadID, rel.ID, err)
	}
	if threadRepo != rel.Repository {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.upsert",
			fmt.Sprintf("Relationship %q repository %q does not match thread %q repository %q.", rel.ID, rel.Repository, rel.ThreadID, threadRepo),
			"Ensure the relationship and its target thread belong to the same repository.",
			nil,
		)
	}

	var itemExists int
	if err := tx.QueryRowContext(
		ctx,
		"SELECT 1 FROM items WHERE repo = ? AND number = ?",
		rel.Repository,
		rel.ItemNumber,
	).Scan(&itemExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(
				domain.ErrorCodeNotFound,
				"relationships.upsert",
				fmt.Sprintf("Item %s#%d for relationship %q was not found.", rel.Repository, rel.ItemNumber, rel.ID),
				fmt.Sprintf("Run 'automain sync --repo %s' before linking relationships to item #%d.", rel.Repository, rel.ItemNumber),
				err,
			)
		}
		return fmt.Errorf("lookup item %s#%d for relationship %q: %w", rel.Repository, rel.ItemNumber, rel.ID, err)
	}

	var (
		existingRepo   string
		existingThread string
		existingItem   int
		existingType   string
	)
	if qErr := tx.QueryRowContext(
		ctx,
		"SELECT repo, thread_id, item_number, rel_type FROM relationships WHERE id = ?",
		string(rel.ID),
	).Scan(&existingRepo, &existingThread, &existingItem, &existingType); qErr == nil {
		if existingRepo != rel.Repository ||
			existingThread != string(rel.ThreadID) ||
			existingItem != rel.ItemNumber ||
			existingType != string(rel.Type) {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.upsert",
				fmt.Sprintf("Relationship ID %q is already assigned to %s#%d (%s on thread %q).", rel.ID, existingRepo, existingItem, existingType, existingThread),
				"Use a unique relationship ID for each distinct (repo, thread_id, item_number, type) tuple.",
				nil,
			)
		}
	} else if !errors.Is(qErr, sql.ErrNoRows) {
		return fmt.Errorf("check existing relationship %q: %w", rel.ID, qErr)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO relationships (
			id, repo, thread_id, item_number, rel_type, explanation,
			confidence, explicitly_linked, verified_by_human, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo, thread_id, item_number, rel_type) DO UPDATE SET
			explanation = excluded.explanation,
			confidence = excluded.confidence,
			explicitly_linked = excluded.explicitly_linked,
			verified_by_human = CASE
				WHEN relationships.verified_by_human != 0 THEN 1
				ELSE excluded.verified_by_human
			END,
			updated_at = excluded.updated_at`,
		string(rel.ID),
		rel.Repository,
		string(rel.ThreadID),
		rel.ItemNumber,
		string(rel.Type),
		rel.Explanation,
		string(rel.Confidence),
		boolToInt(rel.ExplicitlyLinked),
		boolToInt(rel.VerifiedByHuman),
		formatTime(rel.CreatedAt),
		formatTime(rel.UpdatedAt),
	); err != nil {
		return fmt.Errorf("upsert relationship %q: %w", rel.ID, err)
	}

	var effectiveID string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT id FROM relationships
		 WHERE repo = ? AND thread_id = ? AND item_number = ? AND rel_type = ?`,
		rel.Repository,
		string(rel.ThreadID),
		rel.ItemNumber,
		string(rel.Type),
	).Scan(&effectiveID); err != nil {
		return fmt.Errorf("lookup effective id for relationship %q: %w", rel.ID, err)
	}

	if _, err := tx.ExecContext(
		ctx,
		"DELETE FROM relationship_evidence WHERE relationship_id = ?",
		effectiveID,
	); err != nil {
		return fmt.Errorf("clear prior evidence for relationship %q: %w", effectiveID, err)
	}

	for _, ev := range rel.Evidence {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO relationship_evidence (
				relationship_id, source_kind, item_number, comment_id, author, excerpt, url
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			effectiveID,
			string(ev.SourceKind),
			ev.ItemNumber,
			ev.CommentID,
			ev.Author,
			ev.Excerpt,
			ev.URL,
		); err != nil {
			return fmt.Errorf("insert evidence for relationship %q: %w", effectiveID, err)
		}
	}

	return nil
}

// ListRelationships returns relationships matching filter, each populated with its evidence references.
func (s *Store) ListRelationships(ctx context.Context, filter domain.RelationshipFilter) ([]domain.Relationship, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("relationships.list", err)
	}
	if strings.TrimSpace(filter.Repository) != "" {
		if err := domain.ValidateRepoSlug(filter.Repository); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.list",
				err.Error(),
				"Pass the repository in owner/name format.",
				err,
			)
		}
	}
	if filter.ItemNumber < 0 {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.list",
			"Item number (--item) must not be negative.",
			"Pass a positive GitHub item number via --item <number>.",
			nil,
		)
	}
	if filter.Type != "" {
		if err := filter.Type.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.list",
				err.Error(),
				"Pass a valid relationship type: reports_same_problem, proposes_fix, implements_decision, blocks_or_depends, related_context, or duplicate_of.",
				err,
			)
		}
	}

	var query strings.Builder
	query.WriteString("SELECT ")
	query.WriteString(relationshipSelectColumns)
	query.WriteString(" FROM relationships WHERE 1=1")
	args := []any{}

	if strings.TrimSpace(filter.Repository) != "" {
		query.WriteString(" AND repo = ?")
		args = append(args, filter.Repository)
	}
	if strings.TrimSpace(string(filter.ThreadID)) != "" {
		query.WriteString(" AND thread_id = ?")
		args = append(args, strings.TrimSpace(string(filter.ThreadID)))
	}
	if filter.ItemNumber > 0 {
		query.WriteString(" AND item_number = ?")
		args = append(args, filter.ItemNumber)
	}
	if filter.Type != "" {
		query.WriteString(" AND rel_type = ?")
		args = append(args, string(filter.Type))
	}
	if filter.Verified != nil {
		if *filter.Verified {
			query.WriteString(" AND verified_by_human != 0")
		} else {
			query.WriteString(" AND verified_by_human = 0")
		}
	}
	query.WriteString(" ORDER BY created_at ASC, id ASC")

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list relationships: %w", err)
	}
	defer closeRows(rows)

	rels := []domain.Relationship{}
	for rows.Next() {
		rel, err := scanRelationship(rows)
		if err != nil {
			return nil, fmt.Errorf("scan relationship: %w", err)
		}
		rels = append(rels, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relationships: %w", err)
	}
	closeRows(rows)

	for i := range rels {
		evidence, err := s.loadEvidence(ctx, rels[i].ID)
		if err != nil {
			return nil, err
		}
		rels[i].Evidence = evidence
	}

	return rels, nil
}

// GetRelationship returns a single relationship together with its supporting evidence references.
func (s *Store) GetRelationship(ctx context.Context, id domain.RelationshipID) (domain.Relationship, error) {
	if err := ctx.Err(); err != nil {
		return domain.Relationship{}, domain.AsError("relationships.show", err)
	}
	trimmedID := strings.TrimSpace(string(id))
	if trimmedID == "" {
		return domain.Relationship{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.show",
			"Relationship ID (--id) is required.",
			"Pass a non-empty relationship ID via --id <relationship-id>.",
			nil,
		)
	}

	query := "SELECT " + relationshipSelectColumns + " FROM relationships WHERE id = ?"
	rel, err := scanRelationship(s.db.QueryRowContext(ctx, query, trimmedID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Relationship{}, domain.NewError(
			domain.ErrorCodeNotFound,
			"relationships.show",
			fmt.Sprintf("Relationship %q was not found.", trimmedID),
			"Run 'automain relationships list --repo <owner/name>' to inspect available relationship IDs.",
			err,
		)
	}
	if err != nil {
		return domain.Relationship{}, fmt.Errorf("query relationship %q: %w", trimmedID, err)
	}

	evidence, err := s.loadEvidence(ctx, rel.ID)
	if err != nil {
		return domain.Relationship{}, err
	}
	rel.Evidence = evidence
	return rel, nil
}

// VerifyRelationship sets human verification status on a relationship and returns the updated record.
func (s *Store) VerifyRelationship(ctx context.Context, id domain.RelationshipID, verified bool) (domain.Relationship, error) {
	if err := ctx.Err(); err != nil {
		return domain.Relationship{}, domain.AsError("relationships.verify", err)
	}
	trimmedID := strings.TrimSpace(string(id))
	if trimmedID == "" {
		return domain.Relationship{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.verify",
			"Relationship ID (--id) is required.",
			"Pass a non-empty relationship ID via --id <relationship-id>.",
			nil,
		)
	}

	nowStr := formatTime(time.Now().UTC())
	res, err := s.db.ExecContext(
		ctx,
		"UPDATE relationships SET verified_by_human = ?, updated_at = ? WHERE id = ?",
		boolToInt(verified),
		nowStr,
		trimmedID,
	)
	if err != nil {
		return domain.Relationship{}, fmt.Errorf("update relationship %q verification: %w", trimmedID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return domain.Relationship{}, fmt.Errorf("rows affected verifying relationship %q: %w", trimmedID, err)
	}
	if affected == 0 {
		return domain.Relationship{}, domain.NewError(
			domain.ErrorCodeNotFound,
			"relationships.verify",
			fmt.Sprintf("Relationship %q was not found.", trimmedID),
			"Run 'automain relationships list --repo <owner/name>' to inspect available relationship IDs.",
			sql.ErrNoRows,
		)
	}

	return s.GetRelationship(ctx, domain.RelationshipID(trimmedID))
}

func (s *Store) loadEvidence(ctx context.Context, id domain.RelationshipID) ([]domain.EvidenceRef, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT
			source_kind,
			item_number,
			COALESCE(comment_id, ''),
			COALESCE(author, ''),
			excerpt,
			COALESCE(url, '')
		FROM relationship_evidence
		WHERE relationship_id = ?
		ORDER BY id ASC`,
		string(id),
	)
	if err != nil {
		return nil, fmt.Errorf("query evidence for relationship %q: %w", id, err)
	}
	defer closeRows(rows)

	evidence := []domain.EvidenceRef{}
	for rows.Next() {
		var ev domain.EvidenceRef
		var sourceKindStr string
		if err := rows.Scan(
			&sourceKindStr,
			&ev.ItemNumber,
			&ev.CommentID,
			&ev.Author,
			&ev.Excerpt,
			&ev.URL,
		); err != nil {
			return nil, fmt.Errorf("scan evidence for relationship %q: %w", id, err)
		}
		ev.SourceKind = domain.EvidenceSourceKind(sourceKindStr)
		evidence = append(evidence, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence for relationship %q: %w", id, err)
	}

	return evidence, nil
}

func scanRelationship(scanner rowScanner) (domain.Relationship, error) {
	var (
		rel        domain.Relationship
		idStr      string
		threadStr  string
		typeStr    string
		confStr    string
		explicitI  int
		verifiedI  int
		createdRaw string
		updatedRaw string
	)
	if err := scanner.Scan(
		&idStr,
		&rel.Repository,
		&threadStr,
		&rel.ItemNumber,
		&typeStr,
		&rel.Explanation,
		&confStr,
		&explicitI,
		&verifiedI,
		&createdRaw,
		&updatedRaw,
	); err != nil {
		return domain.Relationship{}, err
	}

	rel.ID = domain.RelationshipID(idStr)
	rel.ThreadID = domain.ThreadID(threadStr)
	rel.Type = domain.RelationshipType(typeStr)
	rel.Confidence = domain.ConfidenceLevel(confStr)
	rel.ExplicitlyLinked = intToBool(explicitI)
	rel.VerifiedByHuman = intToBool(verifiedI)
	rel.Evidence = []domain.EvidenceRef{}

	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return domain.Relationship{}, fmt.Errorf("parse relationship %q created_at: %w", rel.ID, err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return domain.Relationship{}, fmt.Errorf("parse relationship %q updated_at: %w", rel.ID, err)
	}
	rel.CreatedAt = createdAt
	rel.UpdatedAt = updatedAt

	return rel, nil
}
