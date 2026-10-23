package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/app"
	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
)

// ServiceFactory builds the application Service once configuration and flags are resolved.
type ServiceFactory func(cfg config.Config) (*app.Service, func(), error)

// Execute parses CLI arguments, runs the requested subcommand, and returns the process exit code.
func Execute(ctx context.Context, args []string, baseCfg config.Config, factory ServiceFactory, stdout, stderr io.Writer) int {
	jsonRequested := detectJSONFlag(args)

	globalCfg, remaining, helpRequested, err := extractGlobalFlags(baseCfg, args)
	if err != nil {
		return writeError(stdout, stderr, jsonRequested, "cli", err)
	}
	if globalCfg.JSONOutput {
		jsonRequested = true
	}

	if len(remaining) == 0 {
		if helpRequested {
			writeMainUsage(stdout)
			return 0
		}
		writeMainUsage(stdout)
		return 0
	}

	subcmd := remaining[0]
	subArgs := remaining[1:]

	if subcmd == "help" || subcmd == "-h" || subcmd == "--help" {
		writeMainUsage(stdout)
		return 0
	}

	if err := globalCfg.Validate(subcmd); err != nil {
		return writeError(stdout, stderr, jsonRequested, subcmd, err)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, globalCfg.Timeout)
	defer cancel()

	svc, cleanup, err := factory(globalCfg)
	if err != nil {
		return writeError(stdout, stderr, jsonRequested, subcmd, err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	switch subcmd {
	case "version":
		if helpRequested {
			fmt.Fprintln(stdout, "Usage: automain version [--json]")
			return 0
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
		return runStatus(cmdCtx, svc, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "sync":
		return runSync(cmdCtx, svc, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "analyze":
		return runAnalyze(cmdCtx, svc, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "threads":
		return runThreads(cmdCtx, svc, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "relationships":
		return runRelationships(cmdCtx, svc, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "stats":
		return runStats(cmdCtx, svc, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	case "serve":
		return runServe(cmdCtx, svc, globalCfg, subArgs, jsonRequested, helpRequested, stdout, stderr)
	default:
		return writeError(stdout, stderr, jsonRequested, subcmd, domain.NewError(
			domain.ErrorCodeInvalidInput,
			subcmd,
			fmt.Sprintf("Unknown command %q.", subcmd),
			"Run 'automain --help' to list supported commands: status, sync, analyze, threads, relationships, stats, serve, version.",
			nil,
		))
	}
}

func runStatus(ctx context.Context, svc *app.Service, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
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

func runSync(ctx context.Context, svc *app.Service, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain sync --repo <owner/name> [--limit <n>] [--since <RFC3339>] [--full] [--json]")
		return 0
	}
	fs := newSubFlagSet("sync")
	limit := fs.Int("limit", 0, "Maximum number of items to fetch")
	sinceStr := fs.String("since", "", "Fetch updates since RFC3339 timestamp")
	full := fs.Bool("full", false, "Ignore stored sync watermark and perform full import")
	if err := fs.Parse(args); err != nil {
		return writeError(stdout, stderr, jsonMode, "sync", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"sync",
			err.Error(),
			"Run 'automain sync --help' to inspect valid flags.",
			err,
		))
	}
	if fs.NArg() > 0 {
		return writeError(stdout, stderr, jsonMode, "sync", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"sync",
			fmt.Sprintf("Unexpected positional argument %q.", fs.Arg(0)),
			"Pass repository via --repo <owner/name>.",
			nil,
		))
	}

	var since *time.Time
	if strings.TrimSpace(*sinceStr) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*sinceStr))
		if err != nil {
			return writeError(stdout, stderr, jsonMode, "sync", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"sync",
				fmt.Sprintf("Invalid --since timestamp %q; expected RFC3339 format.", *sinceStr),
				"Use RFC3339 format such as --since 2026-10-01T00:00:00Z.",
				err,
			))
		}
		since = &parsed
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

func runAnalyze(ctx context.Context, svc *app.Service, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain analyze --repo <owner/name> [--item <number>] [--limit <n>] [--reanalyze] [--json]")
		return 0
	}
	fs := newSubFlagSet("analyze")
	itemNum := fs.Int("item", 0, "Analyze a specific GitHub item number")
	limit := fs.Int("limit", 0, "Maximum number of dirty items to analyze")
	reanalyze := fs.Bool("reanalyze", false, "Re-run analysis even if item content hash matches")
	if err := fs.Parse(args); err != nil {
		return writeError(stdout, stderr, jsonMode, "analyze", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			err.Error(),
			"Run 'automain analyze --help' to inspect valid flags.",
			err,
		))
	}
	if fs.NArg() > 0 {
		return writeError(stdout, stderr, jsonMode, "analyze", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			fmt.Sprintf("Unexpected positional argument %q.", fs.Arg(0)),
			"Pass options using flags (--repo, --item, --limit).",
			nil,
		))
	}

	res, err := svc.Analyze(ctx, domain.AnalyzeOptions{
		Repository: cfg.Repository,
		ItemNumber: *itemNum,
		Limit:      *limit,
		Reanalyze:  *reanalyze,
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

func runThreads(ctx context.Context, svc *app.Service, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
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
		if err := fs.Parse(rest); err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"threads.list",
				err.Error(),
				"Run 'automain threads --help' for usage.",
				err,
			))
		}
		threads, err := svc.ListThreads(ctx, domain.ThreadFilter{
			Repository: cfg.Repository,
			Status:     domain.ThreadStatus(strings.TrimSpace(*status)),
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
		if err := fs.Parse(rest); err != nil {
			return writeError(stdout, stderr, jsonMode, "threads.show", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"threads.show",
				err.Error(),
				"Run 'automain threads show --help' for usage.",
				err,
			))
		}
		detail, err := svc.GetThread(ctx, domain.ThreadID(strings.TrimSpace(*id)))
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

func runRelationships(ctx context.Context, svc *app.Service, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
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
		if err := fs.Parse(rest); err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.list",
				err.Error(),
				"Run 'automain relationships --help' for usage.",
				err,
			))
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
		rels, err := svc.ListRelationships(ctx, domain.RelationshipFilter{
			Repository: cfg.Repository,
			ThreadID:   domain.ThreadID(strings.TrimSpace(*threadID)),
			ItemNumber: *itemNum,
			Type:       domain.RelationshipType(strings.TrimSpace(*relType)),
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
		if err := fs.Parse(rest); err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.show", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.show",
				err.Error(),
				"Run 'automain relationships show --help' for usage.",
				err,
			))
		}
		rel, err := svc.GetRelationship(ctx, domain.RelationshipID(strings.TrimSpace(*id)))
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
		if err := fs.Parse(rest); err != nil {
			return writeError(stdout, stderr, jsonMode, "relationships.verify", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.verify",
				err.Error(),
				"Run 'automain relationships verify --help' for usage.",
				err,
			))
		}
		rel, err := svc.VerifyRelationship(ctx, domain.RelationshipID(strings.TrimSpace(*id)), !*unverify)
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

func runStats(ctx context.Context, svc *app.Service, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
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

func runServe(ctx context.Context, _ *app.Service, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain serve [--listen <host:port>] [--db <path>] [--json]")
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
	return writeError(stdout, stderr, jsonMode, "serve", domain.NewError(
		domain.ErrorCodeNotConfigured,
		"serve",
		"HTTP API server adapter is not wired yet.",
		"Run CLI commands directly or complete WO5 to enable 'automain serve'.",
		nil,
	))
}

func newSubFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func detectJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "-json" || a == "--json=true" || a == "-json=true" {
			return true
		}
	}
	return false
}

// extractGlobalFlags strips global options (--db, --repo, --json, --timeout, --finder, --listen, --help)
// regardless of whether they appear before or after the subcommand name.
func extractGlobalFlags(base config.Config, args []string) (config.Config, []string, bool, error) {
	cfg := base
	var remaining []string
	var helpRequested bool

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			helpRequested = true
		case arg == "--json" || arg == "-json" || arg == "--json=true" || arg == "-json=true":
			cfg.JSONOutput = true
		case arg == "--json=false" || arg == "-json=false":
			cfg.JSONOutput = false
		case arg == "--db" || arg == "-db":
			val, next, err := consumeValue("--db", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.DBPath = val
			i = next
		case strings.HasPrefix(arg, "--db="):
			cfg.DBPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--repo" || arg == "-repo":
			val, next, err := consumeValue("--repo", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.Repository = val
			i = next
		case strings.HasPrefix(arg, "--repo="):
			cfg.Repository = strings.TrimPrefix(arg, "--repo=")
		case arg == "--finder" || arg == "-finder":
			val, next, err := consumeValue("--finder", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.Finder = val
			i = next
		case strings.HasPrefix(arg, "--finder="):
			cfg.Finder = strings.TrimPrefix(arg, "--finder=")
		case arg == "--listen" || arg == "-listen":
			val, next, err := consumeValue("--listen", args, i)
			if err != nil {
				return cfg, nil, false, err
			}
			cfg.ListenAddr = val
			i = next
		case strings.HasPrefix(arg, "--listen="):
			cfg.ListenAddr = strings.TrimPrefix(arg, "--listen=")
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
		case strings.HasPrefix(arg, "--timeout="):
			val := strings.TrimPrefix(arg, "--timeout=")
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
	return args[idx+1], idx + 1, nil
}

func writeMainUsage(w io.Writer) {
	fmt.Fprint(w, `Automain: GitHub issue, pull request, and discussion thread intelligence

Usage:
  automain <command> [flags]

Commands:
  status          Show configuration, database path, and wired adapter status
  sync            Import and incrementally update items from a GitHub repository
  analyze         Classify items, find candidate threads, and verify relationships
  threads         List threads or inspect a thread and its member items (list|show)
  relationships   List, inspect, or verify item-to-thread relationships (list|show|verify)
  stats           Calculate repository activity and relationship graph statistics
  serve           Start the HTTP JSON API and embedded web UI server
  version         Print binary version

Global Flags:
  --db <path>          SQLite database file path (env: AUTOMAIN_DB_PATH)
  --repo <owner/name>  Target GitHub repository (env: AUTOMAIN_REPO)
  --finder <strategy>  Candidate discovery strategy: llm | fts | vector (env: AUTOMAIN_FINDER)
  --listen <addr>      HTTP server listen address (env: AUTOMAIN_LISTEN_ADDR / PORT)
  --timeout <duration> Per-command execution timeout (default: 60s)
  --json               Emit structured JSON output
  -h, --help           Show help for automain or a subcommand
`)
}
