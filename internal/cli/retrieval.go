package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
)

func runIndex(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	action := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	command := "index." + action
	if action != "status" && action != "prepare" && action != "embed" && action != "reconcile" && action != "migrate" {
		return writeError(stdout, stderr, jsonMode, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
			"Unknown index action.", "Use index prepare, status, embed, reconcile, or migrate.", nil))
	}
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain index [status|prepare|embed|reconcile|migrate] --repo <owner/name> [--index-db <path>] [--json]")
		fmt.Fprintln(stdout, "  prepare: copy exact source passages into the separate index; no model calls")
		fmt.Fprintln(stdout, "  embed:   embed pending chunks through OpenRouter (--limit <chunks>, default 100)")
		fmt.Fprintln(stdout, "  reconcile: check missing remote points and resume cleanup; no model calls")
		fmt.Fprintln(stdout, "  migrate: import legacy vectors read-only (--from <path>, --limit <1-10000>, default 100); no model calls")
		return 0
	}
	fs := newSubFlagSet(command)
	limit := 100
	var from string
	if action == "embed" {
		fs.IntVar(&limit, "limit", 100, "Maximum pending chunks to embed")
	}
	if action == "migrate" {
		fs.IntVar(&limit, "limit", 100, "Maximum legacy vector writes")
		fs.StringVar(&from, "from", "", "Explicit legacy vectors.db source, opened read-only")
	}
	if err := parseSubcommandFlags(fs, command, args, "Run 'automain index --help'."); err != nil {
		return writeError(stdout, stderr, jsonMode, command, err)
	}
	if err := validateCLIRequiredRepo(command, cfg.Repository); err != nil {
		return writeError(stdout, stderr, jsonMode, command, err)
	}
	if (action == "embed" || action == "migrate") && (limit < 1 || limit > 10000) {
		return writeError(stdout, stderr, jsonMode, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
			"Chunk limit must be between 1 and 10000.", "Use --limit 100, for example.", nil))
	}
	if action == "migrate" {
		from = strings.TrimSpace(from)
		if from == "" {
			return writeError(stdout, stderr, jsonMode, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				"Migration requires an explicit --from legacy database path.", "Run 'automain index migrate --repo owner/name --from /path/to/vectors.db'.", nil))
		}
		migrationCfg := cfg
		migrationCfg.VectorDBPath = from
		if err := migrationCfg.ValidateDatabasePaths(); err != nil {
			return writeError(stdout, stderr, jsonMode, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				err.Error(), "Choose a legacy file different from the source database and index catalog.", err))
		}
	}
	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, command, err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if action == "status" {
		result, err := svc.IndexStatus(ctx, cfg.Repository)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, command, err)
		}
		if jsonMode {
			return outputRetrievalJSON(stdout, stderr, command, result)
		}
		fmt.Fprintf(stdout, "Index catalog for %s\n  path: %s\n  prepared_chunks: %d\n  pending_deletes: %d\n", result.Repository, cfg.IndexPath(), result.Chunks, result.PendingDeletes)
		if len(result.Spaces) == 0 {
			fmt.Fprintln(stdout, "  No embedding spaces recorded. Run 'automain index embed' or migrate compatible legacy vectors.")
		}
		for _, entry := range result.Spaces {
			fmt.Fprintf(stdout, "  %s/%s (%d dimensions, %s): %d embedded, %d pending\n", entry.Space.Provider, entry.Space.Model, entry.Space.Dimensions, entry.Space.Revision, entry.EmbeddedChunks, entry.PendingChunks)
		}
		return 0
	}
	if action == "migrate" {
		result, err := svc.MigrateIndex(ctx, cfg.Repository, from, limit)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, command, err)
		}
		if jsonMode {
			return outputRetrievalJSON(stdout, stderr, command, result)
		}
		fmt.Fprintf(stdout, "Index migration for %s\n  migrated_chunks: %d\n  reused_chunks: %d\n  skipped_chunks: %d\n  spaces: %d\n", result.Repository, result.MigratedChunks, result.ReusedChunks, result.SkippedChunks, result.Spaces)
		return 0
	}
	var result domain.IndexResult
	if action == "prepare" {
		result, err = svc.PrepareIndex(ctx, cfg.Repository)
	} else if action == "reconcile" {
		result, err = svc.ReconcileIndex(ctx, cfg.Repository)
	} else {
		result, err = svc.EmbedIndex(ctx, cfg.Repository, limit)
	}
	if err != nil {
		return writeError(stdout, stderr, jsonMode, command, err)
	}
	if jsonMode {
		return outputRetrievalJSON(stdout, stderr, command, result)
	}
	fmt.Fprintf(stdout, "Index %s for %s\n  prepared_items: %d\n  prepared_chunks: %d\n  embedded_chunks: %d\n  reused_chunks: %d\n  deleted_points: %d\n", action, result.Repository, result.PreparedItems, result.PreparedChunks, result.EmbeddedChunks, result.ReusedChunks, result.DeletedPoints)
	return 0
}

func runSearch(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain search --repo <owner/name> --query <text> [--limit <1-100>] [--index-db <path>] [--json]")
		fmt.Fprintln(stdout, "Retrieval only. Uses google/gemini-embedding-2 through OpenRouter; requires OPENROUTER_API_KEY.")
		return 0
	}
	fs := newSubFlagSet("search")
	query := fs.String("query", "", "Text to search for")
	limit := fs.Int("limit", 10, "Maximum matching source passages")
	if err := parseSubcommandFlags(fs, "search", args, "Run 'automain search --help'."); err != nil {
		return writeError(stdout, stderr, jsonMode, "search", err)
	}
	if err := validateCLIRequiredRepo("search", cfg.Repository); err != nil {
		return writeError(stdout, stderr, jsonMode, "search", err)
	}
	if strings.TrimSpace(*query) == "" || *limit < 1 || *limit > 100 {
		return writeError(stdout, stderr, jsonMode, "search", domain.NewError(domain.ErrorCodeInvalidInput, "search",
			"Search requires a non-empty --query and a limit between 1 and 100.", "Run 'automain search --help'.", nil))
	}
	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "search", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	result, err := svc.Search(ctx, domain.SearchOptions{Repository: cfg.Repository, Query: *query, Limit: *limit})
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "search", err)
	}
	if jsonMode {
		return outputRetrievalJSON(stdout, stderr, "search", result)
	}
	for _, match := range result.Matches {
		c := match.Chunk
		fmt.Fprintf(stdout, "%.4f %s#%d %s %s [%d:%d]\n  %s\n  %s\n", match.Score, c.Repository, c.ItemNumber, c.SourceKind, c.CommentID, c.StartByte, c.EndByte, c.Text, c.URL)
	}
	if len(result.Matches) == 0 {
		fmt.Fprintln(stdout, "No current source passages matched. Run 'automain index prepare' and embed pending chunks if sources changed.")
	}
	return 0
}

func runSimilar(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain similar --repo <owner/name> (--chunk <id> | --item <number>) [--limit <1-100>] [--json]")
		fmt.Fprintln(stdout, "Grouped item candidates from stored vectors. No embedding key or model calls required.")
		return 0
	}
	fs := newSubFlagSet("similar")
	chunk := fs.String("chunk", "", "Stored source passage ID")
	item := fs.Int("item", 0, "Indexed GitHub item number")
	limit := fs.Int("limit", 10, "Maximum matching items")
	if err := parseSubcommandFlags(fs, "similar", args, "Run 'automain similar --help'."); err != nil {
		return writeError(stdout, stderr, jsonMode, "similar", err)
	}
	if err := validateCLIRequiredRepo("similar", cfg.Repository); err != nil {
		return writeError(stdout, stderr, jsonMode, "similar", err)
	}
	chunkID := strings.TrimSpace(*chunk)
	if flagWasSet(fs, "chunk") == flagWasSet(fs, "item") || (flagWasSet(fs, "chunk") && chunkID == "") || (flagWasSet(fs, "item") && *item <= 0) || *limit < 1 || *limit > 100 {
		return writeError(stdout, stderr, jsonMode, "similar", domain.NewError(domain.ErrorCodeInvalidInput, "similar",
			"Similarity requires exactly one non-empty --chunk or positive --item, and a limit between 1 and 100.", "Run 'automain similar --help'.", nil))
	}
	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "similar", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	result, err := svc.Similar(ctx, domain.SimilarOptions{Repository: cfg.Repository, ChunkID: chunkID, ItemNumber: *item, Limit: *limit})
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "similar", err)
	}
	if jsonMode {
		return outputRetrievalJSON(stdout, stderr, "similar", result)
	}
	for _, match := range result.Matches {
		fmt.Fprintf(stdout, "%.4f %s#%d %s\n", match.Score, result.Repository, match.ItemNumber, match.ItemKind)
		for _, passage := range match.Passages {
			c := passage.Chunk
			fmt.Fprintf(stdout, "  %.4f %s %s [%d:%d]\n  %s\n  %s\n", passage.Score, c.SourceKind, c.CommentID, c.StartByte, c.EndByte, c.Text, c.URL)
		}
	}
	if len(result.Matches) == 0 {
		fmt.Fprintln(stdout, "No current candidate items matched.")
	}
	if result.Truncated {
		fmt.Fprintln(stdout, "Similarity used a bounded subset of source passages; results may be incomplete.")
	}
	return 0
}

func outputRetrievalJSON(stdout, stderr io.Writer, command string, data any) int {
	if err := writeJSONSuccess(stdout, command, data); err != nil {
		return writeError(stdout, stderr, true, command, err)
	}
	return 0
}
