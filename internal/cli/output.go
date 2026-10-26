package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/MRKups/automain/internal/app"
	"github.com/MRKups/automain/internal/domain"
)

// Envelope is the deterministic JSON response structure emitted by all CLI commands when --json is set.
type Envelope struct {
	OK      bool          `json:"ok"`
	Command string        `json:"command"`
	Data    any           `json:"data,omitempty"`
	Error   *domain.Error `json:"error,omitempty"`
}

func writeJSONSuccess(stdout io.Writer, command string, data any) error {
	env := Envelope{
		OK:      true,
		Command: command,
		Data:    data,
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func writeError(stdout, stderr io.Writer, jsonMode bool, command string, err error) int {
	domErr := domain.AsError(command, err)
	if jsonMode {
		env := Envelope{
			OK:      false,
			Command: command,
			Error:   domErr,
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(env); encErr != nil {
			fmt.Fprintf(stderr, "error encoding json response: %v\n", encErr)
		}
	}
	fmt.Fprintf(stderr, "error: %s\n", domErr.Error())
	return domain.ExitCode(domErr)
}

func writeHumanStatus(w io.Writer, rep app.StatusReport) {
	fmt.Fprintf(w, "Automain %s\n\n", rep.Version)
	fmt.Fprintf(w, "Configuration:\n")
	fmt.Fprintf(w, "  db_path:          %s\n", rep.Config.DBPath)
	fmt.Fprintf(w, "  index_db_path:    %s\n", rep.Config.IndexDBPath)
	fmt.Fprintf(w, "  legacy_vector_db_path: %s (read-only migration source)\n", rep.Config.VectorDBPath)
	fmt.Fprintf(w, "  qdrant_host:      %s\n", rep.Config.QdrantHost)
	fmt.Fprintf(w, "  qdrant_port:      %d\n", rep.Config.QdrantPort)
	fmt.Fprintf(w, "  qdrant_tls:       %t\n", rep.Config.QdrantTLS)
	fmt.Fprintf(w, "  qdrant_collection_prefix: %s\n", rep.Config.QdrantCollectionPrefix)
	fmt.Fprintf(w, "  qdrant_api_key_set: %t\n", rep.Config.QdrantAPIKeySet)
	repo := rep.Config.Repository
	if repo == "" {
		repo = "(not set)"
	}
	fmt.Fprintf(w, "  repository:       %s\n", repo)
	fmt.Fprintf(w, "  finder:           %s\n", rep.Config.Finder)
	fmt.Fprintf(w, "  listen_addr:      %s\n", rep.Config.ListenAddr)
	fmt.Fprintf(w, "  timeout:          %s\n", rep.Config.Timeout)
	fmt.Fprintf(w, "  github_token_set: %t\n", rep.Config.GitHubTokenSet)
	fmt.Fprintf(w, "  llm_provider:     %s\n", rep.Config.LLMProvider)
	fmt.Fprintf(w, "  llm_api_key_set:  %t\n", rep.Config.LLMAPIKeySet)
	fmt.Fprintf(w, "  llm_model:        %s\n", rep.Config.LLMModel)
	fmt.Fprintf(w, "  llm_base_url:     %s\n", rep.Config.LLMBaseURL)
	fmt.Fprintf(w, "  embedding_provider:   %s\n", rep.Config.EmbeddingProvider)
	fmt.Fprintf(w, "  embedding_model:      %s\n", rep.Config.EmbeddingModel)
	fmt.Fprintf(w, "  embedding_base_url:   %s\n", rep.Config.EmbeddingBaseURL)
	fmt.Fprintf(w, "  embedding_dimensions: %d\n", rep.Config.EmbeddingDimensions)
	fmt.Fprintf(w, "  embedding_api_key_set: %t\n", rep.Config.EmbeddingAPIKeySet)
	fmt.Fprintf(w, "  decision_provider:    %s\n", rep.Config.DecisionProvider)
	fmt.Fprintf(w, "  decision_model:       %s\n", rep.Config.DecisionModel)
	fmt.Fprintf(w, "  decision_endpoint:    %s\n", rep.Config.DecisionEndpoint)
	fmt.Fprintf(w, "  decision_api_key_set: %t\n\n", rep.Config.DecisionAPIKeySet)

	fmt.Fprintf(w, "Adapters:\n")
	fmt.Fprintf(w, "  storage:               %t (schema v%d)\n", rep.Adapters.Storage, rep.SchemaVersion)
	fmt.Fprintf(w, "  vector_index:          %t (aggregate wiring)\n", rep.Adapters.VectorIndex)
	fmt.Fprintf(w, "  chunk_catalog:         %t (schema v%d)\n", rep.Adapters.ChunkCatalog, rep.IndexSchemaVersion)
	fmt.Fprintf(w, "  vector_store:          %t (Qdrant)\n", rep.Adapters.VectorStore)
	fmt.Fprintf(w, "  embedding_provider:    %t\n", rep.Adapters.EmbeddingProvider)
	fmt.Fprintf(w, "  decision_provider:     %t\n", rep.Adapters.DecisionProvider)
	fmt.Fprintf(w, "  github:                %t\n", rep.Adapters.GitHub)
	fmt.Fprintf(w, "  candidate_finder:      %t\n", rep.Adapters.CandidateFinder)
	fmt.Fprintf(w, "  relationship_analyser: %t\n", rep.Adapters.RelationshipAnalyser)
	fmt.Fprintf(w, "  embedded_ui:           %t\n", rep.Adapters.EmbeddedUI)
}

func writeHumanSyncResult(w io.Writer, res domain.SyncResult) {
	fmt.Fprintf(w, "Synced %s at %s\n", res.Repository, res.SyncedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(w, "  fetched_items:                %d\n", res.FetchedItems)
	fmt.Fprintf(w, "  fetched_comments:             %d\n", res.FetchedComments)
	fmt.Fprintf(w, "  new_items:                    %d\n", res.NewItems)
	fmt.Fprintf(w, "  updated_items:                %d\n", res.UpdatedItems)
	fmt.Fprintf(w, "  unchanged_items:              %d\n", res.UnchangedItems)
	fmt.Fprintf(w, "  dirty_items_pending_analysis: %d\n", res.DirtyItemsPendingAnalysis)
	fmt.Fprintf(w, "  discussions_synced:           %t\n", res.DiscussionsSynced)
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
}

func writeHumanAnalyzeResult(w io.Writer, res domain.AnalyzeResult) {
	fmt.Fprintf(w, "Analyzed %s\n", res.Repository)
	fmt.Fprintf(w, "  analyzed_items:         %d\n", res.AnalyzedItems)
	fmt.Fprintf(w, "  threads_created:        %d\n", res.ThreadsCreated)
	fmt.Fprintf(w, "  threads_updated:        %d\n", res.ThreadsUpdated)
	fmt.Fprintf(w, "  relationships_upserted: %d\n", res.RelationshipsUpserted)
	fmt.Fprintf(w, "  remaining_dirty_items:  %d\n", res.RemainingDirtyItems)
}

func writeHumanThreads(w io.Writer, threads []domain.Thread) {
	if len(threads) == 0 {
		fmt.Fprintln(w, "No threads found.")
		return
	}
	for _, t := range threads {
		fmt.Fprintf(w, "[%s] %s (%s, %d items, rev %d)\n  %s\n", t.ID, t.Title, t.Status, t.ItemCount, t.Revision, t.Summary)
	}
}

func writeHumanThreadDetail(w io.Writer, detail domain.ThreadDetail) {
	t := detail.Thread
	fmt.Fprintf(w, "Thread %s: %s\n", t.ID, t.Title)
	fmt.Fprintf(w, "  Repository: %s\n", t.Repository)
	fmt.Fprintf(w, "  Status:     %s (revision %d)\n", t.Status, t.Revision)
	fmt.Fprintf(w, "  Summary:    %s\n\n", t.Summary)
	fmt.Fprintf(w, "Items (%d):\n", len(detail.Items))
	for _, item := range detail.Items {
		fmt.Fprintf(w, "  #%d [%s/%s] %s\n", item.Number, item.Kind, item.State, item.Title)
	}
	fmt.Fprintf(w, "\nRelationships (%d):\n", len(detail.Relationships))
	for _, rel := range detail.Relationships {
		fmt.Fprintf(w, "  [%s] item #%d -> %s (confidence: %s, verified: %t)\n    %s\n",
			rel.ID, rel.ItemNumber, rel.Type, rel.Confidence, rel.VerifiedByHuman, rel.Explanation)
	}
}

func writeHumanRelationships(w io.Writer, rels []domain.Relationship) {
	if len(rels) == 0 {
		fmt.Fprintln(w, "No relationships found.")
		return
	}
	for _, rel := range rels {
		fmt.Fprintf(w, "[%s] thread=%s item=#%d type=%s confidence=%s verified=%t\n  %s\n",
			rel.ID, rel.ThreadID, rel.ItemNumber, rel.Type, rel.Confidence, rel.VerifiedByHuman, rel.Explanation)
		for _, ev := range rel.Evidence {
			fmt.Fprintf(w, "  evidence (%s #%d): %s\n", ev.SourceKind, ev.ItemNumber, ev.Excerpt)
		}
	}
}

func writeHumanStats(w io.Writer, st domain.RepoStats) {
	fmt.Fprintf(w, "Repository statistics for %s\n", st.Repository)
	fmt.Fprintf(w, "  total_items:                     %d\n", st.TotalItems)
	fmt.Fprintf(w, "  dirty_items_count:               %d\n", st.DirtyItemsCount)
	fmt.Fprintf(w, "  total_threads:                   %d\n", st.TotalThreads)
	fmt.Fprintf(w, "  total_relationships:             %d\n", st.TotalRelationships)
	fmt.Fprintf(w, "  verified_relationships_count:    %d\n", st.VerifiedRelationshipsCount)
	fmt.Fprintf(w, "  non_obvious_relationships_count: %d\n", st.NonObviousRelationshipsCount)
	fmt.Fprintf(w, "  multi_thread_items_count:        %d\n", st.MultiThreadItemsCount)
	if len(st.RepeatedProblems) > 0 {
		fmt.Fprintf(w, "  repeated_problems:               %d\n", len(st.RepeatedProblems))
	}
	if len(st.UnresolvedItems) > 0 {
		fmt.Fprintf(w, "  unresolved_items:                %d\n", len(st.UnresolvedItems))
	}
}

func writeHumanItems(w io.Writer, items []domain.Item) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No items found.")
		return
	}
	for _, item := range items {
		fmt.Fprintf(w, "#%d [%s/%s] dirty=%t %s\n", item.Number, item.Kind, item.State, item.IsDirty(), item.Title)
	}
}

func writeHumanItemDetail(w io.Writer, detail domain.ItemDetail) {
	it := detail.Item
	fmt.Fprintf(w, "Item #%d: %s\n", it.Number, it.Title)
	fmt.Fprintf(w, "  Repository: %s\n", it.Repository)
	fmt.Fprintf(w, "  Kind/State: %s / %s\n", it.Kind, it.State)
	fmt.Fprintf(w, "  Author:     %s\n", it.Author)
	fmt.Fprintf(w, "  Dirty:      %t\n", it.IsDirty())
	if it.URL != "" {
		fmt.Fprintf(w, "  URL:        %s\n", it.URL)
	}
	fmt.Fprintf(w, "\nComments (%d):\n", len(detail.Comments))
	for _, c := range detail.Comments {
		fmt.Fprintf(w, "  [%s] @%s (%s): %s\n", c.CommentID, c.Author, c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), c.Body)
	}
}

func writeHumanGraph(w io.Writer, g domain.GraphView) {
	fmt.Fprintf(w, "Repository graph for %s\n", g.Repository)
	fmt.Fprintf(w, "  threads:       %d\n", len(g.Threads))
	fmt.Fprintf(w, "  items:         %d\n", len(g.Items))
	fmt.Fprintf(w, "  relationships: %d\n", len(g.Relationships))
}

func writeHumanRepositories(w io.Writer, repos []domain.SyncWatermark) {
	if len(repos) == 0 {
		fmt.Fprintln(w, "No repositories found.")
		return
	}
	for _, r := range repos {
		synced := "(never)"
		if r.LastSyncedAt != nil {
			synced = r.LastSyncedAt.Format("2006-01-02T15:04:05Z07:00")
		}
		fmt.Fprintf(w, "%s\titems=%d\tcomments=%d\tlast_synced_at=%s\n", r.Repository, r.ItemCount, r.CommentCount, synced)
	}
}
