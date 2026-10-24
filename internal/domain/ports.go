package domain

import (
	"context"
	"time"
)

// SyncOptions configures a repository synchronization operation.
type SyncOptions struct {
	Repository string     `json:"repository"`
	Limit      int        `json:"limit,omitempty"`
	Since      *time.Time `json:"since,omitempty"`
	Full       bool       `json:"full,omitempty"`
}

// SyncResult reports the outcome of a repository synchronization run.
type SyncResult struct {
	Repository                string    `json:"repository"`
	SyncedAt                  time.Time `json:"synced_at"`
	FetchedItems              int       `json:"fetched_items"`
	FetchedComments           int       `json:"fetched_comments"`
	NewItems                  int       `json:"new_items"`
	UpdatedItems              int       `json:"updated_items"`
	UnchangedItems            int       `json:"unchanged_items"`
	DirtyItemsPendingAnalysis int       `json:"dirty_items_pending_analysis"`
	DiscussionsSynced         bool      `json:"discussions_synced"`
	Warnings                  []string  `json:"warnings,omitempty"`
}

// FetchBatch holds raw items and comments returned by the GitHub adapter.
type FetchBatch struct {
	Items             []Item    `json:"items"`
	Comments          []Comment `json:"comments"`
	DiscussionsSynced bool      `json:"discussions_synced"`
	Warnings          []string  `json:"warnings,omitempty"`
}

// AnalyzeOptions configures an item relationship analysis run.
type AnalyzeOptions struct {
	Repository string `json:"repository"`
	ItemNumber int    `json:"item_number,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Reanalyze  bool   `json:"reanalyze,omitempty"`
	Finder     string `json:"finder,omitempty"`
}

// AnalyzeResult summarizes threads created/updated and relationships persisted.
type AnalyzeResult struct {
	Repository            string `json:"repository"`
	AnalyzedItems         int    `json:"analyzed_items"`
	ThreadsCreated        int    `json:"threads_created"`
	ThreadsUpdated        int    `json:"threads_updated"`
	RelationshipsUpserted int    `json:"relationships_upserted"`
	RemainingDirtyItems   int    `json:"remaining_dirty_items"`
}

// ItemFilter scopes item listing queries.
type ItemFilter struct {
	Repository string    `json:"repository"`
	Kind       ItemKind  `json:"kind,omitempty"`
	State      ItemState `json:"state,omitempty"`
	DirtyOnly  bool      `json:"dirty_only,omitempty"`
	Dirty      *bool     `json:"dirty,omitempty"`
	Limit      int       `json:"limit,omitempty"`
}

// ThreadFilter scopes thread listing queries.
type ThreadFilter struct {
	Repository string       `json:"repository"`
	Status     ThreadStatus `json:"status,omitempty"`
}

// RelationshipFilter scopes relationship listing queries.
type RelationshipFilter struct {
	Repository string           `json:"repository"`
	ThreadID   ThreadID         `json:"thread_id,omitempty"`
	ItemNumber int              `json:"item_number,omitempty"`
	Type       RelationshipType `json:"type,omitempty"`
	Verified   *bool            `json:"verified,omitempty"`
}

// CandidateThread bundles a candidate thread with its current member items for deep analysis.
type CandidateThread struct {
	Thread Thread `json:"thread"`
	Items  []Item `json:"items"`
}

// MatchedThreadLink describes a verified relationship between an analyzed item and an existing thread.
type MatchedThreadLink struct {
	ThreadID             ThreadID         `json:"thread_id"`
	RelationshipType     RelationshipType `json:"relationship_type"`
	Confidence           ConfidenceLevel  `json:"confidence"`
	Explanation          string           `json:"explanation"`
	UpdatedThreadSummary string           `json:"updated_thread_summary"`
	UpdatedThreadStatus  ThreadStatus     `json:"updated_thread_status"`
	Evidence             []EvidenceRef    `json:"evidence"`
}

// NewThreadProposal describes a new thread seeded by an analyzed item.
type NewThreadProposal struct {
	Title            string           `json:"title"`
	Summary          string           `json:"summary"`
	Status           ThreadStatus     `json:"status"`
	RelationshipType RelationshipType `json:"relationship_type"`
	Confidence       ConfidenceLevel  `json:"confidence"`
	Explanation      string           `json:"explanation"`
	Evidence         []EvidenceRef    `json:"evidence"`
}

// AnalysisDecision contains the validated output of the RelationshipAnalyser.
type AnalysisDecision struct {
	MatchedThreads []MatchedThreadLink `json:"matched_threads"`
	NewThreads     []NewThreadProposal `json:"new_threads"`
}

// ItemAnalysisRecord bundles the thread and relationship mutations produced by analyzing one item.
type ItemAnalysisRecord struct {
	Repository      string         `json:"repository"`
	ItemNumber      int            `json:"item_number"`
	AnalyzedHash    string         `json:"analyzed_hash"`
	ThreadsToCreate []Thread       `json:"threads_to_create,omitempty"`
	ThreadsToUpdate []Thread       `json:"threads_to_update,omitempty"`
	Relationships   []Relationship `json:"relationships,omitempty"`
}

// Store is the persistence port implemented by the SQLite storage adapter.
type Store interface {
	SchemaVersion() int
	GetSyncWatermark(ctx context.Context, repo string) (SyncWatermark, error)
	ListRepositories(ctx context.Context) ([]SyncWatermark, error)
	UpsertItemsBatch(ctx context.Context, repo string, batch FetchBatch, syncedAt time.Time) (SyncResult, error)
	GetItem(ctx context.Context, repo string, number int) (Item, error)
	GetItemWithComments(ctx context.Context, repo string, number int) (Item, []Comment, error)
	ListItems(ctx context.Context, filter ItemFilter) ([]Item, error)
	ListDirtyItems(ctx context.Context, repo string, limit int) ([]Item, error)
	CreateThread(ctx context.Context, thread Thread) error
	UpdateThread(ctx context.Context, thread Thread) error
	ListThreads(ctx context.Context, filter ThreadFilter) ([]Thread, error)
	GetThreadDetail(ctx context.Context, id ThreadID) (ThreadDetail, error)
	UpsertRelationship(ctx context.Context, rel Relationship) error
	ListRelationships(ctx context.Context, filter RelationshipFilter) ([]Relationship, error)
	GetRelationship(ctx context.Context, id RelationshipID) (Relationship, error)
	VerifyRelationship(ctx context.Context, id RelationshipID, verified bool) (Relationship, error)
	SaveItemAnalysis(ctx context.Context, record ItemAnalysisRecord) error
	GetRepoStats(ctx context.Context, repo string) (RepoStats, error)
	Close() error
}

// GitHubSource is the ingestion port implemented by the GitHub adapter.
type GitHubSource interface {
	FetchUpdates(ctx context.Context, opts SyncOptions, since *time.Time) (FetchBatch, error)
}

// CandidateFinder narrows existing threads to plausible matches for an item.
type CandidateFinder interface {
	FindCandidates(ctx context.Context, repo string, item Item, threads []ThreadSummary) ([]ThreadID, error)
}

// RelationshipAnalyser evaluates an item and its comments against candidate threads.
type RelationshipAnalyser interface {
	Analyse(ctx context.Context, item Item, comments []Comment, candidates []CandidateThread) (AnalysisDecision, error)
}
