package domain

import (
	"fmt"
	"strings"
	"time"
)

// ThreadID uniquely identifies a persistent thread.
type ThreadID string

// ThreadStatus represents the current resolution state of a thread.
type ThreadStatus string

const (
	ThreadStatusActive   ThreadStatus = "active"
	ThreadStatusResolved ThreadStatus = "resolved"
	ThreadStatusStale    ThreadStatus = "stale"
)

func (s ThreadStatus) Validate() error {
	switch s {
	case ThreadStatusActive, ThreadStatusResolved, ThreadStatusStale:
		return nil
	default:
		return fmt.Errorf("invalid thread status %q", s)
	}
}

// Thread represents a persistent shared problem, request, decision, or proposed solution.
type Thread struct {
	ID         ThreadID     `json:"id"`
	Repository string       `json:"repository"`
	Title      string       `json:"title"`
	Summary    string       `json:"summary"`
	Status     ThreadStatus `json:"status"`
	ItemCount  int          `json:"item_count"`
	Revision   int          `json:"revision"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

func (t Thread) Validate() error {
	if strings.TrimSpace(string(t.ID)) == "" {
		return fmt.Errorf("thread id must not be empty")
	}
	if err := ValidateRepoSlug(t.Repository); err != nil {
		return err
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("thread %q title must not be empty", t.ID)
	}
	if strings.TrimSpace(t.Summary) == "" {
		return fmt.Errorf("thread %q summary must not be empty", t.ID)
	}
	if err := t.Status.Validate(); err != nil {
		return err
	}
	if t.Revision <= 0 {
		return fmt.Errorf("thread %q revision must be positive, got %d", t.ID, t.Revision)
	}
	return nil
}

// ThreadSummary is a compact projection passed to CandidateFinder implementations.
type ThreadSummary struct {
	ID        ThreadID     `json:"id"`
	Title     string       `json:"title"`
	Summary   string       `json:"summary"`
	Status    ThreadStatus `json:"status"`
	ItemCount int          `json:"item_count"`
}

// ThreadDetail combines a Thread with its linked member items and relationships.
type ThreadDetail struct {
	Thread        Thread         `json:"thread"`
	Items         []Item         `json:"items"`
	Relationships []Relationship `json:"relationships"`
}
