package domain

import (
	"fmt"
	"strings"
	"time"
)

// ItemKind identifies the GitHub artifact category.
type ItemKind string

const (
	ItemKindIssue       ItemKind = "issue"
	ItemKindPullRequest ItemKind = "pull_request"
	ItemKindDiscussion  ItemKind = "discussion"
)

func (k ItemKind) Validate() error {
	switch k {
	case ItemKindIssue, ItemKindPullRequest, ItemKindDiscussion:
		return nil
	default:
		return fmt.Errorf("invalid item kind %q", k)
	}
}

// ItemState represents the lifecycle status of a GitHub item.
type ItemState string

const (
	ItemStateOpen     ItemState = "open"
	ItemStateClosed   ItemState = "closed"
	ItemStateMerged   ItemState = "merged"
	ItemStateAnswered ItemState = "answered"
)

func (s ItemState) Validate() error {
	switch s {
	case ItemStateOpen, ItemStateClosed, ItemStateMerged, ItemStateAnswered:
		return nil
	default:
		return fmt.Errorf("invalid item state %q", s)
	}
}

// ValidateRepoSlug checks that repo is formatted as non-empty "owner/name".
func ValidateRepoSlug(repo string) error {
	trimmed := strings.TrimSpace(repo)
	if trimmed == "" {
		return fmt.Errorf("repository slug is empty")
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("repository %q must be formatted as owner/name", repo)
	}
	if strings.ContainsAny(parts[0], " \t\n\r") || strings.ContainsAny(parts[1], " \t\n\r") {
		return fmt.Errorf("repository %q must not contain whitespace", repo)
	}
	return nil
}

// Item represents a GitHub issue, pull request, or discussion.
type Item struct {
	Repository       string    `json:"repository"`
	Number           int       `json:"number"`
	ExternalID       string    `json:"external_id,omitempty"`
	Kind             ItemKind  `json:"kind"`
	State            ItemState `json:"state"`
	Title            string    `json:"title"`
	Body             string    `json:"body"`
	Author           string    `json:"author"`
	Labels           []string  `json:"labels"`
	URL              string    `json:"url"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	ContentHash      string    `json:"content_hash"`
	LastAnalyzedHash string    `json:"last_analyzed_hash,omitempty"`
}

// IsDirty reports whether the item has unanalyzed content changes.
func (i Item) IsDirty() bool {
	return i.LastAnalyzedHash == "" || i.ContentHash != i.LastAnalyzedHash
}

func (i Item) Validate() error {
	if err := ValidateRepoSlug(i.Repository); err != nil {
		return err
	}
	if i.Number <= 0 {
		return fmt.Errorf("item number must be positive, got %d", i.Number)
	}
	if err := i.Kind.Validate(); err != nil {
		return err
	}
	if err := i.State.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(i.Title) == "" {
		return fmt.Errorf("item #%d title must not be empty", i.Number)
	}
	if strings.TrimSpace(i.ContentHash) == "" {
		return fmt.Errorf("item #%d content_hash must not be empty", i.Number)
	}
	return nil
}

// Comment represents a comment or reply on a GitHub item.
type Comment struct {
	Repository string    `json:"repository"`
	ItemNumber int       `json:"item_number"`
	CommentID  string    `json:"comment_id"`
	Author     string    `json:"author"`
	Body       string    `json:"body"`
	URL        string    `json:"url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SyncWatermark records incremental synchronization progress for a repository.
type SyncWatermark struct {
	Repository   string     `json:"repository"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	Cursor       string     `json:"cursor,omitempty"`
	ItemCount    int        `json:"item_count"`
	CommentCount int        `json:"comment_count"`
}
