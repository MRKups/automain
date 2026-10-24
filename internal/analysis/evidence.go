package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MRKups/automain/internal/domain"
)

// DeterministicThreadID generates a stable thread ID from a repository slug and thread title.
func DeterministicThreadID(repo, title string) domain.ThreadID {
	normRepo := strings.ToLower(strings.TrimSpace(repo))
	normTitle := strings.ToLower(strings.Join(strings.Fields(title), " "))
	sum := sha256.Sum256([]byte(normRepo + "\x00" + normTitle))
	return domain.ThreadID("thr-" + hex.EncodeToString(sum[:6]))
}

// DeterministicRelationshipID generates a stable relationship ID from its unique tuple.
func DeterministicRelationshipID(repo string, threadID domain.ThreadID, itemNumber int, relType domain.RelationshipType) domain.RelationshipID {
	payload := fmt.Sprintf(
		"%s\x00%s\x00%d\x00%s",
		strings.ToLower(strings.TrimSpace(repo)),
		strings.TrimSpace(string(threadID)),
		itemNumber,
		strings.TrimSpace(string(relType)),
	)
	sum := sha256.Sum256([]byte(payload))
	return domain.RelationshipID("rel-" + hex.EncodeToString(sum[:6]))
}

// NormalizeRelationshipType maps raw LLM relationship strings to valid domain.RelationshipType constants.
func NormalizeRelationshipType(raw domain.RelationshipType, itemKind domain.ItemKind) (domain.RelationshipType, error) {
	cleaned := strings.ToLower(strings.TrimSpace(string(raw)))
	if cleaned == "" {
		return "", fmt.Errorf("relationship_type must not be empty")
	}
	cleaned = strings.ReplaceAll(cleaned, "-", "_")
	cleaned = strings.ReplaceAll(cleaned, " ", "_")

	candidate := domain.RelationshipType(cleaned)
	if err := candidate.Validate(); err == nil {
		return candidate, nil
	}

	switch cleaned {
	case "same_problem", "problem", "bug", "issue", "reports_problem":
		if itemKind == domain.ItemKindPullRequest {
			return domain.RelationshipProposesFix, nil
		}
		return domain.RelationshipSameProblem, nil
	case "fix", "fixes", "propose_fix", "pull_request", "pr", "solution", "resolves":
		return domain.RelationshipProposesFix, nil
	case "decision", "implements", "architecture", "rfc":
		return domain.RelationshipImplementsDecision, nil
	case "blocks", "blocked_by", "depends", "depends_on", "dependency", "blocker":
		return domain.RelationshipBlocksOrDepends, nil
	case "related", "context", "discussion", "background":
		return domain.RelationshipRelatedContext, nil
	case "duplicate", "dup", "duplicates":
		return domain.RelationshipDuplicateOf, nil
	default:
		return "", fmt.Errorf("invalid relationship type %q", raw)
	}
}

// NormalizeConfidenceLevel maps raw LLM confidence values to valid domain.ConfidenceLevel constants.
func NormalizeConfidenceLevel(raw domain.ConfidenceLevel) (domain.ConfidenceLevel, error) {
	cleaned := strings.ToLower(strings.TrimSpace(string(raw)))
	if cleaned == "" {
		return domain.ConfidenceMedium, nil
	}
	candidate := domain.ConfidenceLevel(cleaned)
	if err := candidate.Validate(); err == nil {
		return candidate, nil
	}
	switch cleaned {
	case "certain", "strong", "very_high":
		return domain.ConfidenceHigh, nil
	case "moderate", "normal", "med":
		return domain.ConfidenceMedium, nil
	case "weak", "uncertain", "speculative":
		return domain.ConfidenceLow, nil
	default:
		return "", fmt.Errorf("invalid confidence level %q", raw)
	}
}

// NormalizeThreadStatus maps raw LLM thread status strings to valid domain.ThreadStatus constants.
func NormalizeThreadStatus(raw domain.ThreadStatus, fallback domain.ThreadStatus) (domain.ThreadStatus, error) {
	cleaned := strings.ToLower(strings.TrimSpace(string(raw)))
	if cleaned == "" {
		if fallback != "" {
			return fallback, nil
		}
		return domain.ThreadStatusActive, nil
	}
	candidate := domain.ThreadStatus(cleaned)
	if err := candidate.Validate(); err == nil {
		return candidate, nil
	}
	switch cleaned {
	case "open", "in_progress", "ongoing":
		return domain.ThreadStatusActive, nil
	case "closed", "merged", "answered", "done", "fixed", "completed":
		return domain.ThreadStatusResolved, nil
	case "inactive", "abandoned", "dormant":
		return domain.ThreadStatusStale, nil
	default:
		return "", fmt.Errorf("invalid thread status %q", raw)
	}
}

// NormalizeEvidenceSourceKind maps raw LLM evidence source_kind values to valid domain.EvidenceSourceKind constants.
func NormalizeEvidenceSourceKind(raw domain.EvidenceSourceKind) (domain.EvidenceSourceKind, error) {
	cleaned := strings.ToLower(strings.TrimSpace(string(raw)))
	cleaned = strings.ReplaceAll(cleaned, "-", "_")
	cleaned = strings.ReplaceAll(cleaned, " ", "_")

	candidate := domain.EvidenceSourceKind(cleaned)
	if err := candidate.Validate(); err == nil {
		return candidate, nil
	}
	switch cleaned {
	case "body", "item", "issue_body", "pr_body", "pull_request_body", "discussion_body":
		return domain.EvidenceSourceItemBody, nil
	case "issue_comment", "pr_comment", "discussion_comment", "reply":
		return domain.EvidenceSourceComment, nil
	default:
		return "", fmt.Errorf("invalid evidence source_kind %q", raw)
	}
}

// ValidateAndGroundEvidenceRefs validates untrusted model evidence references against the
// analyzed item, its comments, and candidate thread member items, and enriches optional author/URL fields.
func ValidateAndGroundEvidenceRefs(
	raw []domain.EvidenceRef,
	item domain.Item,
	comments []domain.Comment,
	candidateItems []domain.Item,
) ([]domain.EvidenceRef, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("at least one source evidence reference is required")
	}

	itemsByNum := make(map[int]domain.Item, len(candidateItems)+1)
	for _, ci := range candidateItems {
		if ci.Number > 0 {
			itemsByNum[ci.Number] = ci
		}
	}
	if item.Number > 0 {
		itemsByNum[item.Number] = item
	}

	commentsByID := make(map[string]domain.Comment, len(comments))
	for _, cm := range comments {
		cid := strings.TrimSpace(cm.CommentID)
		if cid != "" {
			commentsByID[cid] = cm
		}
	}

	out := make([]domain.EvidenceRef, 0, len(raw))
	for idx, ev := range raw {
		sourceKind, err := NormalizeEvidenceSourceKind(ev.SourceKind)
		if err != nil {
			return nil, fmt.Errorf("evidence[%d]: %w", idx, err)
		}
		excerpt := sanitizeProse(ev.Excerpt)
		if excerpt == "" {
			return nil, fmt.Errorf("evidence[%d]: excerpt must not be empty", idx)
		}

		if sourceKind == domain.EvidenceSourceComment {
			commentID := strings.TrimSpace(ev.CommentID)
			if commentID == "" {
				return nil, fmt.Errorf("evidence[%d]: comment evidence must include a non-empty comment_id", idx)
			}
			matchedComment, exists := commentsByID[commentID]
			if !exists {
				return nil, fmt.Errorf("evidence[%d]: unknown comment_id %q for item #%d", idx, commentID, item.Number)
			}
			itemNum := ev.ItemNumber
			if itemNum <= 0 {
				itemNum = matchedComment.ItemNumber
			}
			if matchedComment.ItemNumber > 0 && itemNum != matchedComment.ItemNumber {
				return nil, fmt.Errorf("evidence[%d]: comment_id %q belongs to item #%d, not #%d", idx, commentID, matchedComment.ItemNumber, itemNum)
			}
			author := strings.TrimSpace(ev.Author)
			if author == "" {
				author = matchedComment.Author
			}
			urlStr := strings.TrimSpace(ev.URL)
			if urlStr == "" {
				urlStr = matchedComment.URL
			}
			validated := domain.EvidenceRef{
				SourceKind: domain.EvidenceSourceComment,
				ItemNumber: itemNum,
				CommentID:  commentID,
				Author:     author,
				Excerpt:    excerpt,
				URL:        urlStr,
			}
			if err := validated.Validate(); err != nil {
				return nil, fmt.Errorf("evidence[%d]: %w", idx, err)
			}
			out = append(out, validated)
			continue
		}

		itemNum := ev.ItemNumber
		if itemNum <= 0 {
			return nil, fmt.Errorf("evidence[%d]: item_number must be positive, got %d", idx, itemNum)
		}
		refItem, hasRefItem := itemsByNum[itemNum]
		if !hasRefItem {
			return nil, fmt.Errorf("evidence[%d]: unknown item_number #%d (must be analyzed item #%d or a candidate thread member item)", idx, itemNum, item.Number)
		}
		author := strings.TrimSpace(ev.Author)
		if author == "" {
			author = refItem.Author
		}
		urlStr := strings.TrimSpace(ev.URL)
		if urlStr == "" {
			urlStr = refItem.URL
		}
		validated := domain.EvidenceRef{
			SourceKind: domain.EvidenceSourceItemBody,
			ItemNumber: itemNum,
			Author:     author,
			Excerpt:    excerpt,
			URL:        urlStr,
		}
		if err := validated.Validate(); err != nil {
			return nil, fmt.Errorf("evidence[%d]: %w", idx, err)
		}
		out = append(out, validated)
	}

	return out, nil
}

// DefaultItemExcerpt builds a non-empty source excerpt from an item's body or title.
func DefaultItemExcerpt(item domain.Item) string {
	body := sanitizeProse(item.Body)
	if body != "" {
		return truncateString(body, 240)
	}
	title := sanitizeProse(item.Title)
	if title != "" {
		return truncateString(title, 240)
	}
	return fmt.Sprintf("GitHub %s #%d", item.Kind, item.Number)
}

// DefaultCommentExcerpt builds a non-empty source excerpt from a comment body.
func DefaultCommentExcerpt(cm domain.Comment) string {
	body := sanitizeProse(cm.Body)
	if body != "" {
		return truncateString(body, 240)
	}
	return fmt.Sprintf("Comment %s on item #%d", cm.CommentID, cm.ItemNumber)
}

// DetectExplicitLink reports whether item (or its comments) contains an explicit GitHub
// reference (#<number> or GitHub issue/PR/discussion URL) to another item in threadItems or evidence.
func DetectExplicitLink(
	repo string,
	item domain.Item,
	comments []domain.Comment,
	threadItems []domain.Item,
	evidence []domain.EvidenceRef,
) bool {
	texts := make([]string, 0, 2+len(comments))
	texts = append(texts, item.Title, item.Body)
	for _, cm := range comments {
		texts = append(texts, cm.Body)
	}
	itemRefs := ExtractReferencedItemNumbers(repo, texts...)

	for _, member := range threadItems {
		if member.Number <= 0 || member.Number == item.Number {
			continue
		}
		if _, ok := itemRefs[member.Number]; ok {
			return true
		}
		if item.Number > 0 {
			memberRefs := ExtractReferencedItemNumbers(repo, member.Title, member.Body)
			if _, ok := memberRefs[item.Number]; ok {
				return true
			}
		}
	}

	if len(itemRefs) == 0 {
		return false
	}

	for _, ev := range evidence {
		if ev.ItemNumber > 0 && ev.ItemNumber != item.Number {
			if _, ok := itemRefs[ev.ItemNumber]; ok {
				return true
			}
		}
	}

	return false
}

func isValidRefBoundaryBefore(r rune) bool {
	if r == '#' {
		return false
	}
	return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// ExtractReferencedItemNumbers parses #<number>, <owner/repo>#<number>, and GitHub item URLs from texts.
func ExtractReferencedItemNumbers(repo string, texts ...string) map[int]struct{} {
	refs := make(map[int]struct{})
	normRepo := strings.ToLower(strings.TrimSpace(repo))

	urlPrefixes := []string{
		"/issues/",
		"/pull/",
		"/pulls/",
		"/discussions/",
	}

	for _, rawText := range texts {
		text := strings.ToLower(rawText)
		if text == "" {
			continue
		}

		// 1. Scan for #<digits> preceded by boundary or repo slug.
		for i := 0; i < len(text); i++ {
			if text[i] != '#' {
				continue
			}
			validPrefix := false
			if i == 0 {
				validPrefix = true
			} else {
				prev, _ := utf8.DecodeLastRuneInString(text[:i])
				if isValidRefBoundaryBefore(prev) {
					validPrefix = true
				} else if normRepo != "" && i >= len(normRepo) && text[i-len(normRepo):i] == normRepo {
					if i == len(normRepo) {
						validPrefix = true
					} else {
						beforeRepo, _ := utf8.DecodeLastRuneInString(text[:i-len(normRepo)])
						if isValidRefBoundaryBefore(beforeRepo) {
							validPrefix = true
						}
					}
				}
			}
			if !validPrefix {
				continue
			}

			j := i + 1
			for j < len(text) && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			if j == i+1 {
				continue
			}
			if j < len(text) {
				next, _ := utf8.DecodeRuneInString(text[j:])
				if unicode.IsLetter(next) || unicode.IsDigit(next) || next == '_' {
					continue
				}
			}
			if num, err := strconv.Atoi(text[i+1 : j]); err == nil && num > 0 {
				refs[num] = struct{}{}
			}
		}

		// 2. Scan for GitHub URL paths (/issues/<n>, /pull/<n>, /discussions/<n>).
		for _, prefix := range urlPrefixes {
			searchFrom := 0
			for searchFrom < len(text) {
				idx := strings.Index(text[searchFrom:], prefix)
				if idx < 0 {
					break
				}
				prefixStart := searchFrom + idx
				start := prefixStart + len(prefix)

				// When repo is specified, ensure the URL path belongs to that repo if preceded by a slug.
				if normRepo != "" {
					expectedRepoSuffix := "/" + normRepo
					if prefixStart >= len(expectedRepoSuffix) {
						preceding := text[:prefixStart]
						if strings.Contains(preceding, "github.com/") && !strings.HasSuffix(preceding, expectedRepoSuffix) {
							searchFrom = start
							continue
						}
					}
				}

				end := start
				for end < len(text) && text[end] >= '0' && text[end] <= '9' {
					end++
				}
				if end > start {
					validEnd := true
					if end < len(text) {
						next, _ := utf8.DecodeRuneInString(text[end:])
						if unicode.IsLetter(next) || unicode.IsDigit(next) || next == '_' {
							validEnd = false
						}
					}
					if validEnd {
						if num, err := strconv.Atoi(text[start:end]); err == nil && num > 0 {
							refs[num] = struct{}{}
						}
					}
				}
				searchFrom = start
			}
		}
	}

	return refs
}
