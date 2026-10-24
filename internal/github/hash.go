package github

import (
	"fmt"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

// ComputeContentHash computes a deterministic SHA-256 hex digest over an item
// and its associated comments.
func ComputeContentHash(item domain.Item, comments []domain.Comment) string {
	return domain.ComputeItemContentHash(item, comments)
}

func splitRepoSlug(repo string) (string, string, error) {
	trimmed := strings.TrimSpace(repo)
	if err := domain.ValidateRepoSlug(trimmed); err != nil {
		return "", "", err
	}
	parts := strings.SplitN(trimmed, "/", 2)
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func parseGitHubTime(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid GitHub timestamp %q", raw)
}

func normalizeAuthor(login string) string {
	trimmed := strings.TrimSpace(login)
	if trimmed == "" {
		return "ghost"
	}
	return trimmed
}

func normalizeTitle(title string, kind domain.ItemKind, number int) string {
	trimmed := strings.TrimSpace(title)
	if trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("Untitled %s #%d", kind, number)
}
