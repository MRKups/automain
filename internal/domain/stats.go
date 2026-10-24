package domain

// ProblemThreadStat summarizes a thread ranked by problem and duplicate reports.
type ProblemThreadStat struct {
	ThreadID           ThreadID     `json:"thread_id"`
	Title              string       `json:"title"`
	Status             ThreadStatus `json:"status"`
	ProblemReportCount int          `json:"problem_report_count"`
	TotalItemCount     int          `json:"total_item_count"`
}

// ItemSummary is a compact item projection including its linked thread count.
type ItemSummary struct {
	Repository  string    `json:"repository"`
	Number      int       `json:"number"`
	Kind        ItemKind  `json:"kind"`
	State       ItemState `json:"state"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	URL         string    `json:"url,omitempty"`
	ThreadCount int       `json:"thread_count"`
}

// GraphView projects the repository network of threads, items, and relationships.
type GraphView struct {
	Repository    string         `json:"repository"`
	Threads       []Thread       `json:"threads"`
	Items         []ItemSummary  `json:"items"`
	Relationships []Relationship `json:"relationships"`
}

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
	RepeatedProblems             []ProblemThreadStat      `json:"repeated_problems"`
	MultiThreadItems             []ItemSummary            `json:"multi_thread_items"`
	UnresolvedItems              []ItemSummary            `json:"unresolved_items"`
}
