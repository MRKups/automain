package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MRKups/automain/internal/domain"
)

const relationshipAnalyserSystemPrompt = `You are a repository thread intelligence engine. Given a GitHub item (issue, pull request, or discussion), its comments, and candidate threads with their member items, determine how the item relates to existing threads and whether it seeds any new threads.

Rules:
1. An item may link to zero, one, or multiple existing candidate threads in "matched_threads".
2. Only reference thread IDs that exist in the provided candidate threads list.
3. If the item introduces a distinct problem, bug, feature request, or architectural decision not already covered by a matched candidate thread, propose a new thread in "new_threads".
4. Every matched thread link and new thread proposal MUST include:
   - "relationship_type": one of "reports_same_problem", "proposes_fix", "implements_decision", "blocks_or_depends", "related_context", "duplicate_of"
   - "confidence": one of "high", "medium", "low"
   - "explanation": clear plain-English explanation of why the item has this relationship
   - "evidence": at least one source citation with "source_kind" ("item_body" or "comment"), "item_number", optional "comment_id" (required when source_kind is "comment"), "author", "excerpt" (verbatim quote from the item body or comment), and "url".
5. Write concise, direct technical prose without em-dashes or en-dashes.`

var relationshipAnalyserResponseSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"matched_threads": map[string]any{
			"type": "ARRAY",
			"items": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"thread_id":              map[string]any{"type": "STRING"},
					"relationship_type":      map[string]any{"type": "STRING"},
					"confidence":             map[string]any{"type": "STRING"},
					"explanation":            map[string]any{"type": "STRING"},
					"updated_thread_summary": map[string]any{"type": "STRING"},
					"updated_thread_status":  map[string]any{"type": "STRING"},
					"evidence":               evidenceArraySchema,
				},
				"required": []string{
					"thread_id",
					"relationship_type",
					"confidence",
					"explanation",
					"updated_thread_summary",
					"updated_thread_status",
					"evidence",
				},
			},
		},
		"new_threads": map[string]any{
			"type": "ARRAY",
			"items": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"title":             map[string]any{"type": "STRING"},
					"summary":           map[string]any{"type": "STRING"},
					"status":            map[string]any{"type": "STRING"},
					"relationship_type": map[string]any{"type": "STRING"},
					"confidence":        map[string]any{"type": "STRING"},
					"explanation":       map[string]any{"type": "STRING"},
					"evidence":          evidenceArraySchema,
				},
				"required": []string{
					"title",
					"summary",
					"status",
					"relationship_type",
					"confidence",
					"explanation",
					"evidence",
				},
			},
		},
	},
	"required": []string{"matched_threads", "new_threads"},
}

var evidenceArraySchema = map[string]any{
	"type": "ARRAY",
	"items": map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"source_kind": map[string]any{"type": "STRING"},
			"item_number": map[string]any{"type": "INTEGER"},
			"comment_id":  map[string]any{"type": "STRING"},
			"author":      map[string]any{"type": "STRING"},
			"excerpt":     map[string]any{"type": "STRING"},
			"url":         map[string]any{"type": "STRING"},
		},
		"required": []string{"source_kind", "item_number", "excerpt"},
	},
}

// Analyse evaluates item and its comments against candidate threads using the configured Gemini model.
func (c *Client) Analyse(
	ctx context.Context,
	item domain.Item,
	comments []domain.Comment,
	candidates []domain.CandidateThread,
) (domain.AnalysisDecision, error) {
	if err := ctx.Err(); err != nil {
		return domain.AnalysisDecision{}, domain.AsError("analyze", err)
	}
	if err := domain.ValidateRepoSlug(item.Repository); err != nil {
		return domain.AnalysisDecision{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}
	if item.Number <= 0 {
		return domain.AnalysisDecision{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			fmt.Sprintf("Item number must be positive, got %d.", item.Number),
			"Pass a valid GitHub item.",
			nil,
		)
	}
	if err := c.ValidateConfig(); err != nil {
		return domain.AnalysisDecision{}, err
	}

	userPrompt, err := buildAnalysisUserPrompt(item, comments, candidates)
	if err != nil {
		return domain.AnalysisDecision{}, domain.NewError(
			domain.ErrorCodeInternal,
			"analyze",
			fmt.Sprintf("Failed to build analysis prompt for %s#%d: %v", item.Repository, item.Number, err),
			"Verify item, comment, and candidate thread inputs.",
			err,
		)
	}

	respJSON, err := c.generateJSON(ctx, relationshipAnalyserSystemPrompt, userPrompt, relationshipAnalyserResponseSchema)
	if err != nil {
		return domain.AnalysisDecision{}, err
	}

	rawDecision, err := unmarshalAnalysisDecision(respJSON)
	if err != nil {
		return domain.AnalysisDecision{}, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("Failed to decode analysis decision for %s#%d: %v", item.Repository, item.Number, err),
			"Verify that AUTOMAIN_LLM_MODEL returns structured JSON matching AnalysisDecision.",
			err,
		)
	}

	normalized, err := NormalizeDecision(item, comments, candidates, rawDecision)
	if err != nil {
		return domain.AnalysisDecision{}, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("Invalid analysis decision for %s#%d: %v", item.Repository, item.Number, err),
			"Verify LLM output adheres to relationship type, confidence, status, and evidence rules.",
			err,
		)
	}

	return normalized, nil
}

func buildAnalysisUserPrompt(
	item domain.Item,
	comments []domain.Comment,
	candidates []domain.CandidateThread,
) (string, error) {
	type promptComment struct {
		CommentID string `json:"comment_id"`
		Author    string `json:"author"`
		URL       string `json:"url,omitempty"`
		Body      string `json:"body"`
	}
	type promptMemberItem struct {
		Number int              `json:"number"`
		Kind   domain.ItemKind  `json:"kind"`
		State  domain.ItemState `json:"state"`
		Title  string           `json:"title"`
		Author string           `json:"author"`
		Labels []string         `json:"labels,omitempty"`
		URL    string           `json:"url,omitempty"`
		Body   string           `json:"body"`
	}
	type promptCandidate struct {
		ID        domain.ThreadID     `json:"id"`
		Title     string              `json:"title"`
		Summary   string              `json:"summary"`
		Status    domain.ThreadStatus `json:"status"`
		ItemCount int                 `json:"item_count"`
		Items     []promptMemberItem  `json:"items"`
	}

	pComments := make([]promptComment, 0, len(comments))
	for _, cm := range comments {
		pComments = append(pComments, promptComment{
			CommentID: cm.CommentID,
			Author:    cm.Author,
			URL:       cm.URL,
			Body:      truncateString(cm.Body, 2000),
		})
	}

	pCandidates := make([]promptCandidate, 0, len(candidates))
	for _, cand := range candidates {
		mItems := make([]promptMemberItem, 0, len(cand.Items))
		for _, mi := range cand.Items {
			mItems = append(mItems, promptMemberItem{
				Number: mi.Number,
				Kind:   mi.Kind,
				State:  mi.State,
				Title:  mi.Title,
				Author: mi.Author,
				Labels: mi.Labels,
				URL:    mi.URL,
				Body:   truncateString(mi.Body, 1500),
			})
		}
		pCandidates = append(pCandidates, promptCandidate{
			ID:        cand.Thread.ID,
			Title:     cand.Thread.Title,
			Summary:   cand.Thread.Summary,
			Status:    cand.Thread.Status,
			ItemCount: cand.Thread.ItemCount,
			Items:     mItems,
		})
	}

	payload := struct {
		Repository       string            `json:"repository"`
		Item             promptMemberItem  `json:"item"`
		Comments         []promptComment   `json:"comments"`
		CandidateThreads []promptCandidate `json:"candidate_threads"`
	}{
		Repository: item.Repository,
		Item: promptMemberItem{
			Number: item.Number,
			Kind:   item.Kind,
			State:  item.State,
			Title:  item.Title,
			Author: item.Author,
			Labels: item.Labels,
			URL:    item.URL,
			Body:   truncateString(item.Body, 6000),
		},
		Comments:         pComments,
		CandidateThreads: pCandidates,
	}

	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

type rawMatchedThreadLink struct {
	ThreadID             domain.ThreadID         `json:"thread_id"`
	RelationshipType     domain.RelationshipType `json:"relationship_type"`
	Type                 domain.RelationshipType `json:"type"`
	Confidence           domain.ConfidenceLevel  `json:"confidence"`
	Explanation          string                  `json:"explanation"`
	UpdatedThreadSummary string                  `json:"updated_thread_summary"`
	Summary              string                  `json:"summary"`
	UpdatedThreadStatus  domain.ThreadStatus     `json:"updated_thread_status"`
	Status               domain.ThreadStatus     `json:"status"`
	Evidence             []domain.EvidenceRef    `json:"evidence"`
}

type rawNewThreadProposal struct {
	Title            string                  `json:"title"`
	Summary          string                  `json:"summary"`
	Status           domain.ThreadStatus     `json:"status"`
	RelationshipType domain.RelationshipType `json:"relationship_type"`
	Type             domain.RelationshipType `json:"type"`
	Confidence       domain.ConfidenceLevel  `json:"confidence"`
	Explanation      string                  `json:"explanation"`
	Evidence         []domain.EvidenceRef    `json:"evidence"`
}

func unmarshalAnalysisDecision(data []byte) (domain.AnalysisDecision, error) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil || rawMap == nil {
		return domain.AnalysisDecision{}, fmt.Errorf("analysis decision must be a JSON object")
	}
	_, hasMatched := rawMap["matched_threads"]
	_, hasNew := rawMap["new_threads"]
	if !hasMatched && !hasNew {
		return domain.AnalysisDecision{}, fmt.Errorf("analysis decision JSON is missing matched_threads and new_threads fields")
	}

	var raw struct {
		MatchedThreads []rawMatchedThreadLink `json:"matched_threads"`
		NewThreads     []rawNewThreadProposal `json:"new_threads"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return domain.AnalysisDecision{}, err
	}

	matched := make([]domain.MatchedThreadLink, 0, len(raw.MatchedThreads))
	for _, m := range raw.MatchedThreads {
		relType := m.RelationshipType
		if strings.TrimSpace(string(relType)) == "" {
			relType = m.Type
		}
		summary := strings.TrimSpace(m.UpdatedThreadSummary)
		if summary == "" {
			summary = strings.TrimSpace(m.Summary)
		}
		status := m.UpdatedThreadStatus
		if strings.TrimSpace(string(status)) == "" {
			status = m.Status
		}
		matched = append(matched, domain.MatchedThreadLink{
			ThreadID:             m.ThreadID,
			RelationshipType:     relType,
			Confidence:           m.Confidence,
			Explanation:          m.Explanation,
			UpdatedThreadSummary: summary,
			UpdatedThreadStatus:  status,
			Evidence:             m.Evidence,
		})
	}

	newThreads := make([]domain.NewThreadProposal, 0, len(raw.NewThreads))
	for _, n := range raw.NewThreads {
		relType := n.RelationshipType
		if strings.TrimSpace(string(relType)) == "" {
			relType = n.Type
		}
		newThreads = append(newThreads, domain.NewThreadProposal{
			Title:            n.Title,
			Summary:          n.Summary,
			Status:           n.Status,
			RelationshipType: relType,
			Confidence:       n.Confidence,
			Explanation:      n.Explanation,
			Evidence:         n.Evidence,
		})
	}

	return domain.AnalysisDecision{
		MatchedThreads: matched,
		NewThreads:     newThreads,
	}, nil
}

// NormalizeDecision validates and normalizes an AnalysisDecision against the item, comments, and candidate threads.
func NormalizeDecision(
	item domain.Item,
	comments []domain.Comment,
	candidates []domain.CandidateThread,
	decision domain.AnalysisDecision,
) (domain.AnalysisDecision, error) {
	candidateByID := make(map[domain.ThreadID]domain.CandidateThread, len(candidates))
	for _, c := range candidates {
		tid := domain.ThreadID(strings.TrimSpace(string(c.Thread.ID)))
		if tid != "" {
			c.Thread.ID = tid
			candidateByID[tid] = c
		}
	}

	normMatched := make([]domain.MatchedThreadLink, 0, len(decision.MatchedThreads))
	for _, m := range decision.MatchedThreads {
		tid := domain.ThreadID(strings.TrimSpace(string(m.ThreadID)))
		if tid == "" {
			return domain.AnalysisDecision{}, fmt.Errorf("matched thread link is missing thread_id")
		}

		cand, inCandidates := candidateByID[tid]
		if !inCandidates {
			return domain.AnalysisDecision{}, fmt.Errorf("matched thread %q is not in candidate threads", tid)
		}

		relType, err := NormalizeRelationshipType(m.RelationshipType, item.Kind)
		if err != nil {
			return domain.AnalysisDecision{}, err
		}

		conf, err := NormalizeConfidenceLevel(m.Confidence)
		if err != nil {
			return domain.AnalysisDecision{}, err
		}

		fallbackStatus := domain.ThreadStatusActive
		if cand.Thread.Status != "" {
			fallbackStatus = cand.Thread.Status
		}
		status, err := NormalizeThreadStatus(m.UpdatedThreadStatus, fallbackStatus)
		if err != nil {
			return domain.AnalysisDecision{}, err
		}

		updatedSummary := sanitizeProse(m.UpdatedThreadSummary)
		if updatedSummary == "" {
			updatedSummary = sanitizeProse(cand.Thread.Summary)
		}
		if updatedSummary == "" {
			updatedSummary = DefaultItemExcerpt(item)
		}

		explanation := sanitizeProse(m.Explanation)
		if explanation == "" {
			return domain.AnalysisDecision{}, fmt.Errorf("matched thread %q is missing a relationship explanation", tid)
		}

		evidence, err := ValidateAndGroundEvidenceRefs(m.Evidence, item, comments, cand.Items)
		if err != nil {
			return domain.AnalysisDecision{}, fmt.Errorf("matched thread %q: %w", tid, err)
		}

		normMatched = append(normMatched, domain.MatchedThreadLink{
			ThreadID:             tid,
			RelationshipType:     relType,
			Confidence:           conf,
			Explanation:          explanation,
			UpdatedThreadSummary: updatedSummary,
			UpdatedThreadStatus:  status,
			Evidence:             evidence,
		})
	}

	normNew := make([]domain.NewThreadProposal, 0, len(decision.NewThreads))
	for _, n := range decision.NewThreads {
		title := sanitizeProse(n.Title)
		summary := sanitizeProse(n.Summary)
		explanation := sanitizeProse(n.Explanation)

		if title == "" {
			return domain.AnalysisDecision{}, fmt.Errorf("new thread proposal is missing a title")
		}
		if summary == "" {
			return domain.AnalysisDecision{}, fmt.Errorf("new thread proposal %q is missing a summary", title)
		}
		if explanation == "" {
			return domain.AnalysisDecision{}, fmt.Errorf("new thread proposal %q is missing a relationship explanation", title)
		}

		relType, err := NormalizeRelationshipType(n.RelationshipType, item.Kind)
		if err != nil {
			return domain.AnalysisDecision{}, err
		}

		conf, err := NormalizeConfidenceLevel(n.Confidence)
		if err != nil {
			return domain.AnalysisDecision{}, err
		}

		status, err := NormalizeThreadStatus(n.Status, domain.ThreadStatusActive)
		if err != nil {
			return domain.AnalysisDecision{}, err
		}

		evidence, err := ValidateAndGroundEvidenceRefs(n.Evidence, item, comments, nil)
		if err != nil {
			return domain.AnalysisDecision{}, fmt.Errorf("new thread proposal %q: %w", title, err)
		}

		normNew = append(normNew, domain.NewThreadProposal{
			Title:            title,
			Summary:          summary,
			Status:           status,
			RelationshipType: relType,
			Confidence:       conf,
			Explanation:      explanation,
			Evidence:         evidence,
		})
	}

	if len(normMatched) == 0 && len(normNew) == 0 {
		return domain.AnalysisDecision{}, fmt.Errorf("analysis decision for %s#%d produced no matched threads and no new threads", item.Repository, item.Number)
	}

	return domain.AnalysisDecision{
		MatchedThreads: normMatched,
		NewThreads:     normNew,
	}, nil
}

func sanitizeProse(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	// Replace any em-dash or en-dash generated by an external LLM with standard ASCII punctuation.
	trimmed = strings.ReplaceAll(trimmed, "\u2014", " - ")
	trimmed = strings.ReplaceAll(trimmed, "\u2013", "-")
	return strings.Join(strings.Fields(trimmed), " ")
}
