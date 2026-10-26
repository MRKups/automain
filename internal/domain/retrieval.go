package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// EmbeddingSpace identifies vectors that may safely be compared. Revision must
// change when the model version, task instructions, or input recipe changes.
type EmbeddingSpace struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Revision   string `json:"revision"`
	Dimensions int    `json:"dimensions"`
}

func (s EmbeddingSpace) Validate() error {
	if strings.TrimSpace(s.Provider) == "" || strings.TrimSpace(s.Model) == "" || strings.TrimSpace(s.Revision) == "" {
		return fmt.Errorf("embedding provider, model, and revision must be specified")
	}
	if s.Dimensions <= 0 || s.Dimensions > 65536 {
		return fmt.Errorf("embedding dimensions must be between 1 and 65536")
	}
	return nil
}

func (s EmbeddingSpace) ID() string {
	data, _ := json.Marshal(s) // A struct containing only strings and an integer.
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type EmbeddingPurpose string

const (
	EmbeddingDocument EmbeddingPurpose = "document"
	EmbeddingQuery    EmbeddingPurpose = "query"
)

// EmbeddingProvider is deliberately independent of relationship decision models.
// Embed must return one vector per input, in input order, in the declared space.
// Provider credentials and model configuration are independent of analysis.
type EmbeddingProvider interface {
	Space() EmbeddingSpace
	Embed(ctx context.Context, purpose EmbeddingPurpose, texts []string) ([][]float32, error)
}

// SearchChunk is a rebuildable index record, never the authoritative source.
// StartByte and EndByte address the original title, body, or comment verbatim.
type SearchChunk struct {
	ID          string   `json:"id"`
	Repository  string   `json:"repository"`
	ItemNumber  int      `json:"item_number"`
	ItemKind    ItemKind `json:"item_kind"`
	SourceKind  string   `json:"source_kind"`
	CommentID   string   `json:"comment_id,omitempty"`
	StartByte   int      `json:"start_byte"`
	EndByte     int      `json:"end_byte"`
	Title       string   `json:"title"`
	Text        string   `json:"text"`
	Author      string   `json:"author"`
	URL         string   `json:"url"`
	ContentHash string   `json:"content_hash"`
}

const (
	SearchSourceTitle   = "item_title"
	SearchSourceBody    = "item_body"
	SearchSourceComment = "comment"
)

// InputText adds context without changing Text or its source offsets.
func (c SearchChunk) InputText() string {
	return "title: " + c.Title + " | text: " + c.Text
}

func (c SearchChunk) Validate() error {
	if err := ValidateRepoSlug(c.Repository); err != nil {
		return err
	}
	if err := c.ItemKind.Validate(); err != nil {
		return err
	}
	if c.ID == "" || c.ItemNumber <= 0 || c.ContentHash == "" || strings.TrimSpace(c.Text) == "" {
		return fmt.Errorf("search chunk requires an ID, positive item number, content hash, and text")
	}
	if c.StartByte < 0 || c.EndByte <= c.StartByte || c.EndByte-c.StartByte != len(c.Text) {
		return fmt.Errorf("search chunk offsets must address its exact source text")
	}
	switch c.SourceKind {
	case SearchSourceTitle, SearchSourceBody:
		if c.CommentID != "" {
			return fmt.Errorf("item text must not specify a comment ID")
		}
	case SearchSourceComment:
		if strings.TrimSpace(c.CommentID) == "" {
			return fmt.Errorf("comment text requires a comment ID")
		}
	default:
		return fmt.Errorf("invalid search source kind %q", c.SourceKind)
	}
	return nil
}

type ChunkEmbedding struct {
	Chunk  SearchChunk
	Vector []float32
}

// ValidateVector rejects malformed API responses before anything reaches storage.
func ValidateVector(vector []float32, dimensions int) error {
	if len(vector) != dimensions || dimensions <= 0 {
		return fmt.Errorf("expected %d embedding dimensions, got %d", dimensions, len(vector))
	}
	var norm float64
	for _, value := range vector {
		v := float64(value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("embedding contains a non-finite value")
		}
		norm += v * v
	}
	if norm == 0 {
		return fmt.Errorf("embedding must not be a zero vector")
	}
	return nil
}

// SearchMatch is a retrieval candidate, not a relevance or relationship verdict.
type SearchMatch struct {
	Chunk SearchChunk `json:"chunk"`
	Score float64     `json:"score"` // Cosine similarity, not a confidence/probability.
}

type EmbeddingSpaceStatus struct {
	Space          EmbeddingSpace `json:"space"`
	EmbeddedChunks int            `json:"embedded_chunks"`
	PendingChunks  int            `json:"pending_chunks"`
}

type IndexStatus struct {
	Repository     string                 `json:"repository"`
	Chunks         int                    `json:"chunks"`
	Spaces         []EmbeddingSpaceStatus `json:"spaces"`
	PendingDeletes int                    `json:"pending_deletes"`
}

type IndexResult struct {
	Repository     string `json:"repository"`
	PreparedItems  int    `json:"prepared_items"`
	PreparedChunks int    `json:"prepared_chunks"`
	EmbeddedChunks int    `json:"embedded_chunks"`
	ReusedChunks   int    `json:"reused_chunks"`
	DeletedPoints  int    `json:"deleted_points"`
}

type SearchOptions struct {
	Repository string `json:"repository"`
	Query      string `json:"query"`
	Limit      int    `json:"limit,omitempty"`
}

type SearchResult struct {
	Repository string         `json:"repository"`
	Space      EmbeddingSpace `json:"space"`
	Matches    []SearchMatch  `json:"matches"`
}

// ChunkCatalog keeps passage metadata and durable remote-write/deletion receipts.
// TrackChunks precedes remote writes; ConfirmChunks follows confirmed completion.
// Reconciliation tombstones every tracked old version, including in-flight writes.
type ChunkCatalog interface {
	SchemaVersion() int
	Check(ctx context.Context) error
	ReplaceChunks(ctx context.Context, repo string, chunks []SearchChunk) error
	ListChunks(ctx context.Context, repo, afterID string, limit int) ([]SearchChunk, error)
	GetChunk(ctx context.Context, repo, id string) (SearchChunk, error)
	RegisterSpace(ctx context.Context, space EmbeddingSpace) error
	PendingChunks(ctx context.Context, repo string, space EmbeddingSpace, limit int) ([]SearchChunk, error)
	IndexedChunks(ctx context.Context, repo string, space EmbeddingSpace, afterID string, limit int) ([]SearchChunk, error)
	TrackChunks(ctx context.Context, repo string, space EmbeddingSpace, chunks []SearchChunk) error
	ConfirmChunks(ctx context.Context, repo string, space EmbeddingSpace, chunks []SearchChunk) error
	MarkMissing(ctx context.Context, repo string, space EmbeddingSpace, chunks []SearchChunk) error
	PendingDeletions(ctx context.Context, repo string, limit int) ([]VectorDeletion, error)
	CompleteDeletions(ctx context.Context, deletions []VectorDeletion) error
	IndexStatus(ctx context.Context, repo string) (IndexStatus, error)
}

// VectorStore persists and searches derived vectors. Writes include passage
// payloads so an adapter need not reach into the catalog to construct points.
// SaveEmbeddings must reject mismatched repositories and malformed batches.
// Resource ownership stays with the composition root, not either retrieval port.
type VectorStore interface {
	Check(ctx context.Context) error
	SaveEmbeddings(ctx context.Context, repo string, space EmbeddingSpace, embeddings []ChunkEmbedding) error
	Existing(ctx context.Context, repo string, space EmbeddingSpace, chunks []SearchChunk) (map[string]bool, error)
	DeletePoints(ctx context.Context, repo string, space EmbeddingSpace, pointIDs []string) error
	Search(ctx context.Context, repo string, space EmbeddingSpace, vector []float32, limit int) ([]SearchMatch, error)
	SearchByChunk(ctx context.Context, repo string, space EmbeddingSpace, chunk SearchChunk, limit int) ([]SearchMatch, error)
}

type VectorDeletion struct {
	Repository string         `json:"repository"`
	Space      EmbeddingSpace `json:"space"`
	PointID    string         `json:"point_id"`
}

// PointID is a deterministic UUID for one repository/passage/content version.
// Embedding spaces use separate collections, so a space change cannot alias data.
func PointID(chunk SearchChunk) string {
	data, _ := json.Marshal([]string{chunk.Repository, chunk.ID, chunk.ContentHash})
	sum := sha256.Sum256(data)
	sum[6] = (sum[6] & 0x0f) | 0x80 // UUIDv8: application-defined SHA-256 identity.
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

type SimilarOptions struct {
	Repository string `json:"repository"`
	ChunkID    string `json:"chunk_id,omitempty"`
	ItemNumber int    `json:"item_number,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ItemSearchMatch struct {
	ItemNumber int           `json:"item_number"`
	ItemKind   ItemKind      `json:"item_kind"`
	Score      float64       `json:"score"`
	Passages   []SearchMatch `json:"passages"`
}

type SimilarResult struct {
	Repository    string            `json:"repository"`
	Space         EmbeddingSpace    `json:"space"`
	Matches       []ItemSearchMatch `json:"matches"`
	QueryPassages int               `json:"query_passages"`
	Truncated     bool              `json:"truncated"`
}

type MigrationResult struct {
	Repository     string `json:"repository"`
	MigratedChunks int    `json:"migrated_chunks"`
	ReusedChunks   int    `json:"reused_chunks"`
	SkippedChunks  int    `json:"skipped_chunks"`
	Spaces         int    `json:"spaces"`
}

// LegacyIndex is read-only and never initializes or alters the old vector file.
type LegacyIndex interface {
	ListChunks(ctx context.Context, repo, afterID string, limit int) ([]SearchChunk, error)
	Spaces(ctx context.Context) ([]EmbeddingSpace, error)
	ReadEmbeddings(ctx context.Context, repo string, space EmbeddingSpace, afterID string, limit int) ([]ChunkEmbedding, error)
	Close() error
}

type LegacyIndexOpener func(ctx context.Context, path string) (LegacyIndex, error)
