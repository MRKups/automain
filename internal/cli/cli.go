package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/app"
	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
	"github.com/MRKups/automain/internal/server"
	"github.com/MRKups/automain/web"
)

// ServiceFactory builds the application Service once configuration and flags are resolved.
type ServiceFactory func(ctx context.Context, cfg config.Config) (*app.Service, func(), error)

// Execute parses CLI arguments, runs the requested subcommand, and returns the process exit code.
func Execute(ctx context.Context, args []string, baseCfg config.Config, factory ServiceFactory, stdout, stderr io.Writer) int {
	jsonRequested := baseCfg.JSONOutput || detectJSONFlag(args)

	globalCfg, remaining, helpRequested, err := extractGlobalFlags(baseCfg, args)
	if err != nil {
		return writeError(stdout, stderr, jsonRequested, "cli", err)
	}
	jsonRequested = globalCfg.JSONOutput

	if len(remaining) == 0 {
		if helpRequested || len(args) == 0 {
			writeMainUsage(stdout)
			return 0
		}
		return writeError(stdout, stderr, jsonRequested, "cli", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"cli",
			"Subcommand is required.",
			"Run 'automain --help' to list supported commands: status, sync, items, index, search, similar, decide, analyze, threads, relationships, stats, graph, repositories, serve, version.",
			nil,
		))
	}

	subcmd := remaining[0]
	subArgs := remaining[1:]

	if subcmd == "help" || subcmd == "-h" || subcmd == "--help" {
		writeMainUsage(stdout)
		return 0
	}

	if !helpRequested {
		if err := globalCfg.Validate(subcmd); err != nil {
			return writeError(stdout, stderr, jsonRequested, subcmd, err)
		}
	}

	cmdCtx, cancel := context.WithTimeout(ctx, globalCfg.Timeout)
	defer cancel()

	switch subcmd {
	case "version":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain version [--json]")
			return 0
		}
		if len(subArgs) > 0 {
			return writeError(stdout, stderr, jsonRequested, "version", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"version",
				fmt.Sprintf("Unexpected argument %q for version.", subArgs[0]),
				"Run 'automain version [--json]'.",
				nil,
			))
		}
		if jsonRequested {
			if err := writeJSONSuccess(stdout, "version", map[string]string{"version": app.Version}); err != nil {
				return writeError(stdout, stderr, jsonRequested, "version", err)
			}
			return 0
		}
		fmt.Fprintln(stdout, app.Version)
		return 0

	case "status":
		return runStatus(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "sync":
		return runSync(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "items":
		return runItems(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "analyze":
		return runAnalyze(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "index":
		return runIndex(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "search":
		return runSearch(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "similar":
		return runSimilar(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "decide":
		return runDecide(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "threads":
		return runThreads(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "relationships":
		return runRelationships(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "stats":
		return runStats(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "graph":
		return runGraph(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "repositories", "repos":
		return runRepositories(cmdCtx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "serve":
		return runServe(ctx, factory, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	default:
		return writeError(stdout, stderr, jsonRequested, subcmd, domain.NewError(
			domain.ErrorCodeInvalidInput,
			subcmd,
			fmt.Sprintf("Unknown command %q.", subcmd),
			"Run 'automain --help' to list supported commands, including index and search.",
			nil,
		))
	}
}

func runStatus(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain status [--db <path>] [--repo <owner/name>] [--json]")
		return 0
	}
	if len(args) > 0 {
		return writeError(stdout, stderr, jsonMode, "status", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"status",
			fmt.Sprintf("Unexpected argument %q for status.", args[0]),
			"Run 'automain status --help' for usage.",
			nil,
		))
	}
	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "status", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	rep, err := svc.Status(ctx)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "status", err)
	}
	if jsonMode {
		if err := writeJSONSuccess(stdout, "status", rep); err != nil {
			return writeError(stdout, stderr, jsonMode, "status", err)
		}
		return 0
	}
	writeHumanStatus(stdout, rep)
	return 0
}

func runSync(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain sync --repo <owner/name> [--limit <n>] [--since <RFC3339>] [--full] [--json]")
		return 0
	}
	fs := newSubFlagSet("sync")
	limit := fs.Int("limit", 0, "Maximum number of items to fetch")
	sinceStr := fs.String("since", "", "Fetch updates since RFC3339 timestamp")
	full := fs.Bool("full", false, "Ignore --since and fetch without an update cutoff")
	if err := parseSubcommandFlags(fs, "sync", args, "Run 'automain sync --help' to inspect valid flags."); err != nil {
		return writeError(stdout, stderr, jsonMode, "sync", err)
	}

	var since *time.Time
	if strings.TrimSpace(*sinceStr) != "" {
		rawSince := strings.TrimSpace(*sinceStr)
		parsed, err := time.Parse(time.RFC3339, rawSince)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339Nano, rawSince)
		}
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "sync", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"sync",
				fmt.Sprintf("Invalid --since timestamp %q; expected RFC3339 format.", *sinceStr),
				"Use RFC3339 format such as --since 2026-10-01T00:00:00Z.",
				err,
			))
		}
		utc := parsed.UTC()
		since = &utc
	}
	if err := validateCLIRequiredRepo("sync", cfg.Repository); err != nil {
		return writeError(stdout, stderr, jsonMode, "sync", err)
	}
	if *limit < 0 {
		return writeError(stdout, stderr, jsonMode, "sync", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"sync",
			"Sync item limit (--limit) must not be negative.",
			"Pass a positive integer for --limit or omit it.",
			nil,
		))
	}

	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "sync", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	res, err := svc.Sync(ctx, domain.SyncOptions{
		Repository: cfg.Repository,
		Limit:      *limit,
		Since:      since,
		Full:       *full,
	})
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "sync", err)
	}
	if jsonMode {
		if err := writeJSONSuccess(stdout, "sync", res); err != nil {
			return writeError(stdout, stderr, jsonMode, "sync", err)
		}
		return 0
	}
	writeHumanSyncResult(stdout, res)
	return 0
}

func runAnalyze(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain analyze --repo <owner/name> [--item <number>] [--limit <n>] [--reanalyze] [--finder llm|fts|vector] [--timeout <duration>] [--json]")
		return 0
	}
	fs := newSubFlagSet("analyze")
	itemNum := fs.Int("item", 0, "Analyze a specific GitHub item number")
	limit := fs.Int("limit", 0, "Maximum number of dirty items to analyze")
	reanalyze := fs.Bool("reanalyze", false, "Re-run analysis even if item content hash matches")
	if err := parseSubcommandFlags(fs, "analyze", args, "Run 'automain analyze --help' to inspect valid flags."); err != nil {
		return writeError(stdout, stderr, jsonMode, "analyze", err)
	}
	if err := validateCLIRequiredRepo("analyze", cfg.Repository); err != nil {
		return writeError(stdout, stderr, jsonMode, "analyze", err)
	}
	if *itemNum < 0 || (flagWasSet(fs, "item") && *itemNum <= 0) {
		return writeError(stdout, stderr, jsonMode, "analyze", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			"Item number (--item) must be a positive integer.",
			"Pass a valid GitHub issue, PR, or discussion number via --item <number>.",
			nil,
		))
	}
	if *limit < 0 {
		return writeError(stdout, stderr, jsonMode, "analyze", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			"Analysis item limit (--limit) must not be negative.",
			"Pass a positive integer for --limit or omit it.",
			nil,
		))
	}

	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "analyze", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	res, err := svc.Analyze(ctx, domain.AnalyzeOptions{
		Repository: cfg.Repository,
		ItemNumber: *itemNum,
		Limit:      *limit,
		Reanalyze:  *reanalyze,
		Finder:     cfg.Finder,
	})
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "analyze", err)
	}
	if jsonMode {
		if err := writeJSONSuccess(stdout, "analyze", res); err != nil {
			return writeError(stdout, stderr, jsonMode, "analyze", err)
		}
		return 0
	}
	writeHumanAnalyzeResult(stdout, res)
	return 0
}

func runItems(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		rest = args[1:]
	}

	switch action {
	case "list":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain items [list] --repo <owner/name> [--kind issue|pull_request|discussion] [--state open|closed|merged|answered] [--dirty [true|false]] [--limit <n>] [--json]")
			return 0
		}
		normalizedRest := normalizeBoolFlagArgs(rest, "dirty")
		fs := newSubFlagSet("items list")
		kind := fs.String("kind", "", "Filter items by kind (issue|pull_request|discussion)")
		state := fs.String("state", "", "Filter items by state (open|closed|merged|answered)")
		dirty := fs.Bool("dirty", false, "Filter items by dirty analysis state")
		limit := fs.Int("limit", 0, "Maximum number of items to return")
		if err := parseSubcommandFlags(fs, "items.list", normalizedRest, "Run 'automain items --help' for usage."); err != nil {
			return writeError(stdout, stderr, jsonMode, "items.list", err)
		}
		if err := validateCLIRequiredRepo("items.list", cfg.Repository); err != nil {
			return writeError(stdout, stderr, jsonMode, "items.list", err)
		}
		kindVal := domain.ItemKind(strings.TrimSpace(*kind))
		if kindVal != "" {
			if err := kindVal.Validate(); err != nil {
				return writeError(stdout, stderr, jsonMode, "items.list", domain.NewError(
					domain.ErrorCodeInvalidInput,
					"items.list",
					err.Error(),
					"Use --kind issue, --kind pull_request, or --kind discussion.",
					err,
				))
			}
		}
		stateVal := domain.ItemState(strings.TrimSpace(*state))
		if stateVal != "" {
			if err := stateVal.Validate(); err != nil {
				return writeError(stdout, stderr, jsonMode, "items.list", domain.NewError(
					domain.ErrorCodeInvalidInput,
					"items.list",
					err.Error(),
					"Use --state open, --state closed, --state merged, or --state answered.",
					err,
				))
			}
		}
		if *limit < 0 {
			return writeError(stdout, stderr, jsonMode, "items.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.list",
				"Item limit (--limit) must not be negative.",
				"Pass a non-negative integer for --limit or omit it.",
				nil,
			))
		}
		var dirtyPtr *bool
		if flagWasSet(fs, "dirty") {
			b := *dirty
			dirtyPtr = &b
		}

		svc, cleanup, err := factory(ctx, cfg)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "items.list", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		items, err := svc.ListItems(ctx, domain.ItemFilter{
			Repository: cfg.Repository,
			Kind:       kindVal,
			State:      stateVal,
			DirtyOnly:  dirtyPtr != nil && *dirtyPtr,
			Dirty:      dirtyPtr,
			Limit:      *limit,
		})
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "items.list", err)
		}
		if items == nil {
			items = []domain.Item{}
		}
		if jsonMode {
			if err := writeJSONSuccess(stdout, "items.list", items); err != nil {
				return writeError(stdout, stderr, jsonMode, "items.list", err)
			}
			return 0
		}
		writeHumanItems(stdout, items)
		return 0

	case "show":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain items show --repo <owner/name> --number <n> [--json]")
			return 0
		}
		fs := newSubFlagSet("items show")
		number := fs.Int("number", 0, "GitHub item number to inspect")
		itemAlias := fs.Int("item", 0, "GitHub item number to inspect (alias for --number)")
		if err := parseSubcommandFlags(fs, "items.show", rest, "Run 'automain items show --help' for usage."); err != nil {
			return writeError(stdout, stderr, jsonMode, "items.show", err)
		}
		if err := validateCLIRequiredRepo("items.show", cfg.Repository); err != nil {
			return writeError(stdout, stderr, jsonMode, "items.show", err)
		}
		targetNum := *number
		if targetNum == 0 && flagWasSet(fs, "item") {
			targetNum = *itemAlias
		}
		if targetNum <= 0 {
			return writeError(stdout, stderr, jsonMode, "items.show", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.show",
				"Item number (--number) must be a positive integer.",
				"Pass a positive GitHub item number via --number <n>.",
				nil,
			))
		}

		svc, cleanup, err := factory(ctx, cfg)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "items.show", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		item, comments, err := svc.GetItem(ctx, cfg.Repository, targetNum)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "items.show", err)
		}
		if comments == nil {
			comments = []domain.Comment{}
		}
		detail := domain.ItemDetail{
			Item:     item,
			Comments: comments,
		}
		if jsonMode {
			if err := writeJSONSuccess(stdout, "items.show", detail); err != nil {
				return writeError(stdout, stderr, jsonMode, "items.show", err)
			}
			return 0
		}
		writeHumanItemDetail(stdout, detail)
		return 0

	default:
		return writeError(stdout, stderr, jsonMode, "items", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items",
			fmt.Sprintf("Unknown items subcommand %q.", action),
			"Supported subcommands: 'automain items list' and 'automain items show --number <n>'.",
			nil,
		))
	}
}

func normalizeBoolFlagArgs(args []string, flagName string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--"+flagName || arg == "-"+flagName) && i+1 < len(args) {
			next := strings.ToLower(strings.TrimSpace(args[i+1]))
			if next == "true" || next == "false" || next == "1" || next == "0" {
				out = append(out, arg+"="+next)
				i++
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

func runThreads(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		rest = args[1:]
	}

	switch action {
	case "list":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain threads [list] --repo <owner/name> [--status active|resolved|stale] [--json]")
			return 0
		}
		fs := newSubFlagSet("threads list")
		status := fs.String("status", "", "Filter threads by status (active|resolved|stale)")
		if err := parseSubcommandFlags(fs, "threads.list", rest, "Run 'automain threads --help' for usage."); err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.list", err)
		}
		if err := validateCLIRequiredRepo("threads.list", cfg.Repository); err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.list", err)
		}
		statusVal := domain.ThreadStatus(strings.TrimSpace(*status))
		if statusVal != "" {
			if err := statusVal.Validate(); err != nil {
				return writeError(stdout, stderr, jsonMode, "threads.list", domain.NewError(
					domain.ErrorCodeInvalidInput,
					"threads.list",
					err.Error(),
					"Use --status active, --status resolved, or --status stale.",
					err,
				))
			}
		}

		svc, cleanup, err := factory(ctx, cfg)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.list", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		threads, err := svc.ListThreads(ctx, domain.ThreadFilter{
			Repository: cfg.Repository,
			Status:     statusVal,
		})
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.list", err)
		}
		if jsonMode {
			if err := writeJSONSuccess(stdout, "threads.list", threads); err != nil {
				return writeError(stdout, stderr, jsonMode, "threads.list", err)
			}
			return 0
		}
		writeHumanThreads(stdout, threads)
		return 0

	case "show":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain threads show --id <thread-id> [--json]")
			return 0
		}
		fs := newSubFlagSet("threads show")
		id := fs.String("id", "", "Thread ID to inspect")
		if err := parseSubcommandFlags(fs, "threads.show", rest, "Run 'automain threads show --help' for usage."); err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.show", err)
		}
		trimmedID := strings.TrimSpace(*id)
		if trimmedID == "" {
			return writeError(stdout, stderr, jsonMode, "threads.show", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"threads.show",
				"Thread ID (--id) is required.",
				"Pass a non-empty thread ID via --id <thread-id>.",
				nil,
			))
		}

		svc, cleanup, err := factory(ctx, cfg)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.show", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		detail, err := svc.GetThread(ctx, domain.ThreadID(trimmedID))
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.show", err)
		}
		if jsonMode {
			if err := writeJSONSuccess(stdout, "threads.show", detail); err != nil {
				return writeError(stdout, stderr, jsonMode, "threads.show", err)
			}
			return 0
		}
		writeHumanThreadDetail(stdout, detail)
		return 0

	default:
		return writeError(stdout, stderr, jsonMode, "threads", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"threads",
			fmt.Sprintf("Unknown threads subcommand %q.", action),
			"Supported subcommands: 'automain threads list' and 'automain threads show --id <id>'.",
			nil,
		))
	}
}

func runRelationships(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		rest = args[1:]
	}

	switch action {
	case "list":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain relationships [list] --repo <owner/name> [--thread <id>] [--item <number>] [--type <type>] [--verified true|false] [--json]")
			return 0
		}
		fs := newSubFlagSet("relationships list")
		threadID := fs.String("thread", "", "Filter by thread ID")
		itemNum := fs.Int("item", 0, "Filter by GitHub item number")
		relType := fs.String("type", "", "Filter by relationship type")
		verifiedStr := fs.String("verified", "", "Filter by human verification (true|false)")
		if err := parseSubcommandFlags(fs, "relationships.list", rest, "Run 'automain relationships --help' for usage."); err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.list", err)
		}
		var verifiedPtr *bool
		if v := strings.TrimSpace(*verifiedStr); v != "" {
			switch v {
			case "true":
				b := true
				verifiedPtr = &b
			case "false":
				b := false
				verifiedPtr = &b
			default:
				return writeError(stdout, stderr, jsonMode, "relationships.list", domain.NewError(
					domain.ErrorCodeInvalidInput,
					"relationships.list",
					fmt.Sprintf("Invalid --verified value %q; expected true or false.", v),
					"Pass --verified true or --verified false.",
					nil,
				))
			}
		}
		if err := validateCLIRequiredRepo("relationships.list", cfg.Repository); err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.list", err)
		}
		if *itemNum < 0 || (flagWasSet(fs, "item") && *itemNum <= 0) {
			return writeError(stdout, stderr, jsonMode, "relationships.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.list",
				"Item number (--item) must be a positive integer.",
				"Pass a positive GitHub item number via --item <number>.",
				nil,
			))
		}
		typeVal := domain.RelationshipType(strings.TrimSpace(*relType))
		if typeVal != "" {
			if err := typeVal.Validate(); err != nil {
				return writeError(stdout, stderr, jsonMode, "relationships.list", domain.NewError(
					domain.ErrorCodeInvalidInput,
					"relationships.list",
					err.Error(),
					"Pass a valid relationship type: reports_same_problem, proposes_fix, implements_decision, blocks_or_depends, related_context, or duplicate_of.",
					err,
				))
			}
		}

		svc, cleanup, err := factory(ctx, cfg)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.list", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		rels, err := svc.ListRelationships(ctx, domain.RelationshipFilter{
			Repository: cfg.Repository,
			ThreadID:   domain.ThreadID(strings.TrimSpace(*threadID)),
			ItemNumber: *itemNum,
			Type:       typeVal,
			Verified:   verifiedPtr,
		})
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.list", err)
		}
		if jsonMode {
			if err := writeJSONSuccess(stdout, "relationships.list", rels); err != nil {
				return writeError(stdout, stderr, jsonMode, "relationships.list", err)
			}
			return 0
		}
		writeHumanRelationships(stdout, rels)
		return 0

	case "show":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain relationships show --id <relationship-id> [--json]")
			return 0
		}
		fs := newSubFlagSet("relationships show")
		id := fs.String("id", "", "Relationship ID to inspect")
		if err := parseSubcommandFlags(fs, "relationships.show", rest, "Run 'automain relationships show --help' for usage."); err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.show", err)
		}
		trimmedID := strings.TrimSpace(*id)
		if trimmedID == "" {
			return writeError(stdout, stderr, jsonMode, "relationships.show", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.show",
				"Relationship ID (--id) is required.",
				"Pass a non-empty relationship ID via --id <relationship-id>.",
				nil,
			))
		}

		svc, cleanup, err := factory(ctx, cfg)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.show", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		rel, err := svc.GetRelationship(ctx, domain.RelationshipID(trimmedID))
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.show", err)
		}
		if jsonMode {
			if err := writeJSONSuccess(stdout, "relationships.show", rel); err != nil {
				return writeError(stdout, stderr, jsonMode, "relationships.show", err)
			}
			return 0
		}
		writeHumanRelationships(stdout, []domain.Relationship{rel})
		return 0

	case "verify":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain relationships verify --id <relationship-id> [--unverify] [--json]")
			return 0
		}
		fs := newSubFlagSet("relationships verify")
		id := fs.String("id", "", "Relationship ID to verify")
		unverify := fs.Bool("unverify", false, "Clear human verification flag")
		if err := parseSubcommandFlags(fs, "relationships.verify", rest, "Run 'automain relationships verify --help' for usage."); err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.verify", err)
		}
		trimmedID := strings.TrimSpace(*id)
		if trimmedID == "" {
			return writeError(stdout, stderr, jsonMode, "relationships.verify", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.verify",
				"Relationship ID (--id) is required.",
				"Pass a non-empty relationship ID via --id <relationship-id>.",
				nil,
			))
		}

		svc, cleanup, err := factory(ctx, cfg)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.verify", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		rel, err := svc.VerifyRelationship(ctx, domain.RelationshipID(trimmedID), !*unverify)
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.verify", err)
		}
		if jsonMode {
			if err := writeJSONSuccess(stdout, "relationships.verify", rel); err != nil {
				return writeError(stdout, stderr, jsonMode, "relationships.verify", err)
			}
			return 0
		}
		writeHumanRelationships(stdout, []domain.Relationship{rel})
		return 0

	default:
		return writeError(stdout, stderr, jsonMode, "relationships", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships",
			fmt.Sprintf("Unknown relationships subcommand %q.", action),
			"Supported subcommands: 'list', 'show --id <id>', and 'verify --id <id>'.",
			nil,
		))
	}
}

func runStats(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain stats --repo <owner/name> [--json]")
		return 0
	}
	if len(args) > 0 {
		return writeError(stdout, stderr, jsonMode, "stats", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"stats",
			fmt.Sprintf("Unexpected argument %q for stats.", args[0]),
			"Run 'automain stats --repo <owner/name> [--json]'.",
			nil,
		))
	}
	if err := validateCLIRequiredRepo("stats", cfg.Repository); err != nil {
		return writeError(stdout, stderr, jsonMode, "stats", err)
	}

	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "stats", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	st, err := svc.Stats(ctx, cfg.Repository)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "stats", err)
	}
	if jsonMode {
		if err := writeJSONSuccess(stdout, "stats", st); err != nil {
			return writeError(stdout, stderr, jsonMode, "stats", err)
		}
		return 0
	}
	writeHumanStats(stdout, st)
	return 0
}

func runGraph(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain graph --repo <owner/name> [--json]")
		return 0
	}
	if len(args) > 0 {
		return writeError(stdout, stderr, jsonMode, "graph", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"graph",
			fmt.Sprintf("Unexpected argument %q for graph.", args[0]),
			"Run 'automain graph --repo <owner/name> [--json]'.",
			nil,
		))
	}
	if err := validateCLIRequiredRepo("graph", cfg.Repository); err != nil {
		return writeError(stdout, stderr, jsonMode, "graph", err)
	}

	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "graph", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	g, err := svc.Graph(ctx, cfg.Repository)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "graph", err)
	}
	if jsonMode {
		if err := writeJSONSuccess(stdout, "graph", g); err != nil {
			return writeError(stdout, stderr, jsonMode, "graph", err)
		}
		return 0
	}
	writeHumanGraph(stdout, g)
	return 0
}

func runRepositories(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		rest = args[1:]
	}
	if action != "list" {
		return writeError(stdout, stderr, jsonMode, "repositories.list", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"repositories.list",
			fmt.Sprintf("Unknown repositories subcommand %q.", action),
			"Run 'automain repositories [list] [--json]'.",
			nil,
		))
	}
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain repositories [list] [--db <path>] [--json]")
		return 0
	}
	if len(rest) > 0 {
		return writeError(stdout, stderr, jsonMode, "repositories.list", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"repositories.list",
			fmt.Sprintf("Unexpected argument %q for repositories.", rest[0]),
			"Run 'automain repositories [list] [--json]'.",
			nil,
		))
	}

	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "repositories.list", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	repos, err := svc.ListRepositories(ctx)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "repositories.list", err)
	}
	if repos == nil {
		repos = []domain.SyncWatermark{}
	}
	if jsonMode {
		if err := writeJSONSuccess(stdout, "repositories.list", repos); err != nil {
			return writeError(stdout, stderr, jsonMode, "repositories.list", err)
		}
		return 0
	}
	writeHumanRepositories(stdout, repos)
	return 0
}

func runServe(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain serve [--listen <host:port>] [--db <path>] [--repo <owner/name>] [--json]")
		return 0
	}
	if err := ctx.Err(); err != nil {
		return writeError(stdout, stderr, jsonMode, "serve", err)
	}
	if len(args) > 0 {
		return writeError(stdout, stderr, jsonMode, "serve", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"serve",
			fmt.Sprintf("Unexpected argument %q for serve.", args[0]),
			"Pass listen address via --listen <host:port>.",
			nil,
		))
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return writeError(stdout, stderr, jsonMode, "serve", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"serve",
			"Listen address (--listen) must not be empty.",
			"Pass --listen 127.0.0.1:8080 or set AUTOMAIN_LISTEN_ADDR.",
			nil,
		))
	}

	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "serve", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	onListen := func(addr net.Addr) {
		boundAddr := addr.String()
		if jsonMode {
			if encErr := writeJSONSuccess(stdout, "serve", map[string]any{
				"listen_addr": boundAddr,
				"url":         "http://" + boundAddr,
				"db_path":     cfg.DBPath,
				"repository":  cfg.Repository,
				"ui_embedded": web.HasIndex(),
			}); encErr != nil {
				fmt.Fprintf(stderr, "error encoding json response: %v\n", encErr)
			}
			return
		}
		fmt.Fprintf(stdout, "Listening on http://%s (db: %s, ui_embedded: %t)\n", boundAddr, cfg.DBPath, web.HasIndex())
	}

	if err := server.ListenAndServe(ctx, cfg.ListenAddr, svc, cfg.Timeout, onListen); err != nil {
		return writeError(stdout, stderr, jsonMode, "serve", err)
	}
	return 0
}

func validateCLIRequiredRepo(operation, repo string) error {
	trimmed := strings.TrimSpace(repo)
	if trimmed == "" {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			"Repository is required for this operation.",
			"Pass --repo <owner/name> or set AUTOMAIN_REPO=<owner/name>.",
			nil,
		)
	}
	if err := domain.ValidateRepoSlug(trimmed); err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			err.Error(),
			"Pass the repository in owner/name format, for example --repo cli/cli.",
			err,
		)
	}
	return nil
}

func newSubFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

type boolFlag interface {
	IsBoolFlag() bool
}

// parseSubcommandFlags validates that value-taking flags do not swallow subsequent flags,
// parses fs, and rejects any leftover positional arguments.
func parseSubcommandFlags(fs *flag.FlagSet, operation string, args []string, recovery string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		trimmed := strings.TrimLeft(arg, "-")
		if eqIdx := strings.IndexByte(trimmed, '='); eqIdx >= 0 {
			name := trimmed[:eqIdx]
			val := trimmed[eqIdx+1:]
			f := fs.Lookup(name)
			if f == nil {
				continue
			}
			if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
				continue
			}
			if strings.TrimSpace(val) == "" || (strings.HasPrefix(val, "-") && !isSignedInteger(val)) {
				return domain.NewError(
					domain.ErrorCodeInvalidInput,
					operation,
					fmt.Sprintf("Flag --%s requires a valid value, got %q.", name, val),
					recovery,
					nil,
				)
			}
			continue
		}

		f := fs.Lookup(trimmed)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 >= len(args) {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				operation,
				fmt.Sprintf("Flag %s requires a value.", arg),
				recovery,
				nil,
			)
		}
		next := args[i+1]
		if strings.TrimSpace(next) == "" || (strings.HasPrefix(next, "-") && !isSignedInteger(next)) {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				operation,
				fmt.Sprintf("Flag %s requires a value, got %q.", arg, next),
				recovery,
				nil,
			)
		}
		i++
	}

	if err := fs.Parse(args); err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			err.Error(),
			recovery,
			err,
		)
	}
	if fs.NArg() > 0 {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			fmt.Sprintf("Unexpected argument %q.", fs.Arg(0)),
			recovery,
			nil,
		)
	}
	return nil
}

func isSignedInteger(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func detectJSONFlag(args []string) bool {
	jsonMode := false
	for _, a := range args {
		if a == "--" {
			break
		}
		switch strings.ToLower(a) {
		case "--json", "-json", "--json=true", "-json=true", "--json=1", "-json=1":
			jsonMode = true
		case "--json=false", "-json=false", "--json=0", "-json=0":
			jsonMode = false
		}
	}
	return jsonMode
}

// extractGlobalFlags strips global options (--db, --index-db, --vector-db, --repo, --json, --timeout, --finder, --listen, --help)
// regardless of whether they appear before or after the subcommand name.
func extractGlobalFlags(base config.Config, args []string) (config.Config, []string, bool, error) {
	cfg := base
	var remaining []string
	var helpRequested bool

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		lowerArg := strings.ToLower(arg)
		switch {
		case arg == "-h" || arg == "--help":
			helpRequested = true
		case lowerArg == "--json" || lowerArg == "-json" || lowerArg == "--json=true" || lowerArg == "-json=true" || lowerArg == "--json=1" || lowerArg == "-json=1":
			cfg.JSONOutput = true
		case lowerArg == "--json=false" || lowerArg == "-json=false" || lowerArg == "--json=0" || lowerArg == "-json=0":
			cfg.JSONOutput = false
		case hasFlagValuePrefix(lowerArg, "json"):
			eqIdx := strings.IndexByte(arg, '=')
			val := arg[eqIdx+1:]
			return cfg, nil, false, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"cli",
				fmt.Sprintf("Invalid --json boolean value %q; expected true, false, 1, or 0.", val),
				"Pass --json, --json=true, or --json=false.",
				nil,
			)
		case arg == "--db" || arg == "-db":
			val, next, err := consumeValue("--db", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.DBPath = val
			i = next
		case hasFlagValuePrefix(arg, "db"):
			val, err := extractInlineValue("--db", arg)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.DBPath = val
		case arg == "--index-db" || arg == "-index-db":
			val, next, err := consumeValue("--index-db", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.IndexDBPath = val
			i = next
		case hasFlagValuePrefix(arg, "index-db"):
			val, err := extractInlineValue("--index-db", arg)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.IndexDBPath = val
		case arg == "--vector-db" || arg == "-vector-db":
			val, next, err := consumeValue("--vector-db", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.VectorDBPath = val
			i = next
		case hasFlagValuePrefix(arg, "vector-db"):
			val, err := extractInlineValue("--vector-db", arg)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.VectorDBPath = val
		case arg == "--repo" || arg == "-repo":
			val, next, err := consumeValue("--repo", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.Repository = val
			i = next
		case hasFlagValuePrefix(arg, "repo"):
			val, err := extractInlineValue("--repo", arg)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.Repository = val
		case arg == "--finder" || arg == "-finder":
			val, next, err := consumeValue("--finder", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.Finder = val
			i = next
		case hasFlagValuePrefix(arg, "finder"):
			val, err := extractInlineValue("--finder", arg)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.Finder = val
		case arg == "--listen" || arg == "-listen":
			val, next, err := consumeValue("--listen", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.ListenAddr = val
			i = next
		case hasFlagValuePrefix(arg, "listen"):
			val, err := extractInlineValue("--listen", arg)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.ListenAddr = val
		case arg == "--timeout" || arg == "-timeout":
			val, next, err := consumeValue("--timeout", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			dur, parseErr := time.ParseDuration(val)
			if parseErr != nil {
				return cfg, nil, false, domain.NewError(
					domain.ErrorCodeInvalidInput,
					"cli",
					fmt.Sprintf("Invalid --timeout duration %q.", val),
					"Pass a valid Go duration string such as --timeout 30s or --timeout 2m.",
					parseErr,
				)
			}
			cfg.Timeout = dur
			i = next
		case hasFlagValuePrefix(arg, "timeout"):
			eqIdx := strings.IndexByte(arg, '=')
			val := arg[eqIdx+1:]
			dur, parseErr := time.ParseDuration(val)
			if parseErr != nil {
				return cfg, nil, false, domain.NewError(
					domain.ErrorCodeInvalidInput,
					"cli",
					fmt.Sprintf("Invalid --timeout duration %q.", val),
					"Pass a valid Go duration string such as --timeout 30s or --timeout 2m.",
					parseErr,
				)
			}
			cfg.Timeout = dur
		default:
			remaining = append(remaining, arg)
		}
	}

	return cfg, remaining, helpRequested, nil
}

func hasFlagValuePrefix(arg, name string) bool {
	return strings.HasPrefix(arg, "--"+name+"=") || strings.HasPrefix(arg, "-"+name+"=")
}

func extractInlineValue(flagName, arg string) (string, error) {
	eqIdx := strings.IndexByte(arg, '=')
	val := arg[eqIdx+1:]
	if strings.TrimSpace(val) == "" || strings.HasPrefix(val, "-") {
		return "", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"cli",
			fmt.Sprintf("Flag %s requires a non-empty value, got %q.", flagName, val),
			fmt.Sprintf("Provide a valid value for %s or run 'automain --help'.", flagName),
			nil,
		)
	}
	return val, nil
}

func consumeValue(flagName string, args []string, idx int) (string, int, error) {
	if idx+1 >= len(args) {
		return "", idx, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"cli",
			fmt.Sprintf("Flag %s requires a value.", flagName),
			fmt.Sprintf("Provide a value after %s or run 'automain --help'.", flagName),
			nil,
		)
	}
	val := args[idx+1]
	if strings.TrimSpace(val) == "" || (strings.HasPrefix(val, "-") && !(flagName == "--timeout" && len(val) > 1 && val[1] >= '0' && val[1] <= '9')) {
		return "", idx, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"cli",
			fmt.Sprintf("Flag %s requires a value, got %q.", flagName, val),
			fmt.Sprintf("Provide a value after %s or run 'automain --help'.", flagName),
			nil,
		)
	}
	return val, idx + 1, nil
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func writeMainUsage(w io.Writer) {
	fmt.Fprint(w, `Automain: GitHub issue, pull request, and discussion thread intelligence

Usage:
  automain <command> [flags]

Commands:
  status          Show configuration, database path, and wired adapter status
  sync            Import GitHub items and comments, optionally filtered by --since
  items           List repository items or inspect an item with comments (list|show)
  index           Manage the passage catalog and Qdrant index (prepare|status|embed|reconcile|migrate)
  search          Retrieve similar source passages without deciding relationships
  similar         Find related items using stored passage vectors; no model calls
  decide          Ask Jev typed questions about supplied state (no relationship writes)
  analyze         Use the legacy LLM pipeline to propose threads and relationships
  threads         List threads or inspect a thread and its member items (list|show)
  relationships   List, inspect, or verify item-to-thread relationships (list|show|verify)
  stats           Calculate repository activity and relationship graph statistics
  graph           Export the full repository thread-item relationship network
  repositories    List repositories and sync watermarks stored in SQLite
  serve           Start the HTTP JSON API and embedded web UI server
  version         Print binary version

Global Flags:
  --db <path>          SQLite database file path (env: AUTOMAIN_DB_PATH)
  --index-db <path>    SQLite passage catalog (env: AUTOMAIN_INDEX_DB_PATH; default: index.db next to --db)
  --vector-db <path>   Deprecated legacy read-only source path, never opened at startup; migrate requires --from
  --repo <owner/name>  Target GitHub repository (env: AUTOMAIN_REPO)
  --finder <strategy>  Legacy analysis finder: llm | fts | vector (TF-IDF, not embeddings)
                      Environment: AUTOMAIN_FINDER; does not affect search
  --listen <addr>      HTTP server listen address (env: AUTOMAIN_LISTEN_ADDR / PORT)
  --timeout <duration> Command / serve API request timeout (default: 60s)
  --json               Emit structured JSON output
  -h, --help           Show help for automain or a subcommand
`)
}
