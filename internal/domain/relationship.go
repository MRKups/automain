package domain

import (
	"fmt"
	"strings"
	"time"
)

// RelationshipID uniquely identifies a link between an item and a thread.
type RelationshipID string

// RelationshipType classifies how an item relates to a thread.
type RelationshipType string

const (
	RelationshipSameProblem        RelationshipType = "reports_same_problem"
	RelationshipProposesFix        RelationshipType = "proposes_fix"
	RelationshipImplementsDecision RelationshipType = "implements_decision"
	RelationshipBlocksOrDepends    RelationshipType = "blocks_or_depends"
	RelationshipRelatedContext     RelationshipType = "related_context"
	RelationshipDuplicateOf        RelationshipType = "duplicate_of"
)

func (t RelationshipType) Validate() error {
	switch t {
	case RelationshipSameProblem,
		RelationshipProposesFix,
		RelationshipImplementsDecision,
		RelationshipBlocksOrDepends,
		RelationshipRelatedContext,
		RelationshipDuplicateOf:
		return nil
	default:
		return fmt.Errorf("invalid relationship type %q", t)
	}
}

// ConfidenceLevel indicates certainty of an inferred relationship.
type ConfidenceLevel string

const (
	ConfidenceHigh   ConfidenceLevel = "high"
	ConfidenceMedium ConfidenceLevel = "medium"
	ConfidenceLow    ConfidenceLevel = "low"
)

func (c ConfidenceLevel) Validate() error {
	switch c {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		return nil
	default:
		return fmt.Errorf("invalid confidence level %q", c)
	}
}

// EvidenceSourceKind distinguishes item body citations from comment citations.
type EvidenceSourceKind string

const (
	EvidenceSourceItemBody EvidenceSourceKind = "item_body"
	EvidenceSourceComment  EvidenceSourceKind = "comment"
)

func (k EvidenceSourceKind) Validate() error {
	switch k {
	case EvidenceSourceItemBody, EvidenceSourceComment:
		return nil
	default:
		return fmt.Errorf("invalid evidence source kind %q", k)
	}
}

// EvidenceRef points to the specific GitHub item or comment supporting a relationship.
type EvidenceRef struct {
	SourceKind EvidenceSourceKind `json:"source_kind"`
	ItemNumber int                `json:"item_number"`
	CommentID  string             `json:"comment_id,omitempty"`
	Author     string             `json:"author,omitempty"`
	Excerpt    string             `json:"excerpt"`
	URL        string             `json:"url,omitempty"`
}

func (e EvidenceRef) Validate() error {
	if err := e.SourceKind.Validate(); err != nil {
		return err
	}
	if e.ItemNumber <= 0 {
		return fmt.Errorf("evidence item_number must be positive, got %d", e.ItemNumber)
	}
	if e.SourceKind == EvidenceSourceComment && strings.TrimSpace(e.CommentID) == "" {
		return fmt.Errorf("comment evidence for item #%d must include comment_id", e.ItemNumber)
	}
	if strings.TrimSpace(e.Excerpt) == "" {
		return fmt.Errorf("evidence for item #%d must include a non-empty excerpt", e.ItemNumber)
	}
	return nil
}

// Relationship links an Item to a Thread with a type, explanation, and verifiable evidence.
type Relationship struct {
	ID               RelationshipID   `json:"id"`
	Repository       string           `json:"repository"`
	ThreadID         ThreadID         `json:"thread_id"`
	ItemNumber       int              `json:"item_number"`
	Type             RelationshipType `json:"type"`
	Explanation      string           `json:"explanation"`
	Confidence       ConfidenceLevel  `json:"confidence"`
	Evidence         []EvidenceRef    `json:"evidence"`
	ExplicitlyLinked bool             `json:"explicitly_linked"`
	VerifiedByHuman  bool             `json:"verified_by_human"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// Validate enforces the domain invariant that every relationship has a valid type,
// a clear explanation, and at least one verifiable source evidence citation.
func (r Relationship) Validate() error {
	if strings.TrimSpace(string(r.ID)) == "" {
		return fmt.Errorf("relationship id must not be empty")
	}
	if err := ValidateRepoSlug(r.Repository); err != nil {
		return err
	}
	if strings.TrimSpace(string(r.ThreadID)) == "" {
		return fmt.Errorf("relationship %q thread_id must not be empty", r.ID)
	}
	if r.ItemNumber <= 0 {
		return fmt.Errorf("relationship %q item_number must be positive, got %d", r.ID, r.ItemNumber)
	}
	if err := r.Type.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Explanation) == "" {
		return fmt.Errorf("relationship %q must include a non-empty explanation", r.ID)
	}
	if err := r.Confidence.Validate(); err != nil {
		return err
	}
	if len(r.Evidence) == 0 {
		return fmt.Errorf("relationship %q must include at least one source evidence reference", r.ID)
	}
	for idx, ev := range r.Evidence {
		if err := ev.Validate(); err != nil {
			return fmt.Errorf("relationship %q evidence[%d]: %w", r.ID, idx, err)
		}
	}
	return nil
}
