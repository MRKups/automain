package domain

// RepoStats summarizes ingested items, threads, relationships, and dirty state for a repository.
type RepoStats struct {
	Repository                   string                   `json:"repository"`
	TotalItems                   int                      `json:"total_items"`
	ItemsByKind                  map[ItemKind]int         `json:"items_by_kind"`
	ItemsByState                 map[ItemState]int        `json:"items_by_state"`
	DirtyItemsCount              int                      `json:"dirty_items_count"`
	TotalThreads                 int                      `json:"total_threads"`
	ThreadsByStatus              map[ThreadStatus]int     `json:"threads_by_status"`
	TotalRelationships           int                      `json:"total_relationships"`
	RelationshipsByType          map[RelationshipType]int `json:"relationships_by_type"`
	VerifiedRelationshipsCount   int                      `json:"verified_relationships_count"`
	NonObviousRelationshipsCount int                      `json:"non_obvious_relationships_count"`
	MultiThreadItemsCount        int                      `json:"multi_thread_items_count"`
}
