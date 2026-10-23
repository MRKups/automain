package app

import (
	"context"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
)

const Version = "0.1.0-dev"

// Adapters groups the external effect ports injected into the application Service.
type Adapters struct {
	Store      domain.Store
	GitHub     domain.GitHubSource
	Finder     domain.CandidateFinder
	Analyser   domain.RelationshipAnalyser
	EmbeddedUI bool
}

// StatusConfig is a safe, secret-redacted projection of runtime configuration.
type StatusConfig struct {
	DBPath         string `json:"db_path"`
	Repository     string `json:"repository,omitempty"`
	Finder         string `json:"finder"`
	ListenAddr     string `json:"listen_addr"`
	Timeout        string `json:"timeout"`
	GitHubTokenSet bool   `json:"github_token_set"`
	LLMAPIKeySet   bool   `json:"llm_api_key_set"`
	LLMModel       string `json:"llm_model"`
}

// AdapterStatus reports which effect ports are wired in the current binary composition.
type AdapterStatus struct {
	Storage              bool `json:"storage"`
	GitHub               bool `json:"github"`
	CandidateFinder      bool `json:"candidate_finder"`
	RelationshipAnalyser bool `json:"relationship_analyser"`
	EmbeddedUI           bool `json:"embedded_ui"`
}

// StatusReport summarizes application readiness, configuration, and adapter availability.
type StatusReport struct {
	Version  string        `json:"version"`
	Config   StatusConfig  `json:"config"`
	Adapters AdapterStatus `json:"adapters"`
}

// Service coordinates domain operations across the CLI and HTTP API surfaces.
type Service struct {
	cfg      config.Config
	adapters Adapters
}

// NewService constructs the shared application service facade.
func NewService(cfg config.Config, adapters Adapters) *Service {
	return &Service{
		cfg:      cfg,
		adapters: adapters,
	}
}

// Status returns the current configuration projection and wired adapter states.
func (s *Service) Status(ctx context.Context) (StatusReport, error) {
	if err := ctx.Err(); err != nil {
		return StatusReport{}, domain.AsError("status", err)
	}
	return StatusReport{
		Version: Version,
		Config: StatusConfig{
			DBPath:         s.cfg.DBPath,
			Repository:     s.cfg.Repository,
			Finder:         s.cfg.Finder,
			ListenAddr:     s.cfg.ListenAddr,
			Timeout:        s.cfg.Timeout.String(),
			GitHubTokenSet: s.cfg.GitHubTokenSet(),
			LLMAPIKeySet:   s.cfg.LLMAPIKeySet(),
			LLMModel:       s.cfg.LLMModel,
		},
		Adapters: AdapterStatus{
			Storage:              s.adapters.Store != nil,
			GitHub:               s.adapters.GitHub != nil,
			CandidateFinder:      s.adapters.Finder != nil,
			RelationshipAnalyser: s.adapters.Analyser != nil,
			EmbeddedUI:           s.adapters.EmbeddedUI,
		},
	}, nil
}

// Sync imports repository issues, pull requests, discussions, and comments.
func (s *Service) Sync(ctx context.Context, opts domain.SyncOptions) (domain.SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.SyncResult{}, domain.AsError("sync", err)
	}
	repo := s.resolveRepo(opts.Repository)
	if err := requireRepo("sync", repo); err != nil {
		return domain.SyncResult{}, err
	}
	if opts.Limit < 0 {
		return domain.SyncResult{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"sync",
			"Sync item limit (--limit) must not be negative.",
			"Pass a positive integer for --limit or omit it.",
			nil,
		)
	}
	if s.adapters.Store == nil || s.adapters.GitHub == nil {
		return domain.SyncResult{}, domain.NewError(
			domain.ErrorCodeNotConfigured,
			"sync",
			"SQLite storage adapter and GitHub ingestion adapter are not wired yet.",
			"Run 'automain status' to inspect adapter availability; storage and GitHub adapters are scheduled in WO2 and WO3.",
			nil,
		)
	}

	opts.Repository = repo
	var since *time.Time
	if opts.Since != nil {
		since = opts.Since
	} else if !opts.Full {
		wm, err := s.adapters.Store.GetSyncWatermark(ctx, repo)
		if err != nil {
			return domain.SyncResult{}, domain.AsError("sync", err)
		}
		since = wm.LastSyncedAt
	}

	batch, err := s.adapters.GitHub.FetchUpdates(ctx, opts, since)
	if err != nil {
		return domain.SyncResult{}, domain.AsError("sync", err)
	}
	res, err := s.adapters.Store.UpsertItemsBatch(ctx, repo, batch, time.Now().UTC())
	if err != nil {
		return domain.SyncResult{}, domain.AsError("sync", err)
	}
	return res, nil
}

// Analyze classifies dirty items, discovers candidate threads, and persists verified relationships.
func (s *Service) Analyze(ctx context.Context, opts domain.AnalyzeOptions) (domain.AnalyzeResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.AnalyzeResult{}, domain.AsError("analyze", err)
	}
	repo := s.resolveRepo(opts.Repository)
	if err := requireRepo("analyze", repo); err != nil {
		return domain.AnalyzeResult{}, err
	}
	if opts.ItemNumber < 0 {
		return domain.AnalyzeResult{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			"Item number (--item) must be a positive integer.",
			"Pass a valid GitHub issue, PR, or discussion number via --item <number>.",
			nil,
		)
	}
	if opts.Limit < 0 {
		return domain.AnalyzeResult{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			"Analysis item limit (--limit) must not be negative.",
			"Pass a positive integer for --limit or omit it.",
			nil,
		)
	}
	if s.adapters.Store == nil || s.adapters.Finder == nil || s.adapters.Analyser == nil {
		return domain.AnalyzeResult{}, domain.NewError(
			domain.ErrorCodeNotConfigured,
			"analyze",
			"Storage, candidate finder, and relationship analyser adapters are not wired yet.",
			"Run 'automain status' to inspect adapter availability; storage and discovery adapters are scheduled in WO2 and WO4.",
			nil,
		)
	}
	return domain.AnalyzeResult{}, domain.NewError(
		domain.ErrorCodeNotConfigured,
		"analyze",
		"Thread analysis pipeline is not wired yet.",
		"Complete WO4 to enable candidate discovery and relationship analysis.",
		nil,
	)
}

// ListThreads returns persistent threads matching filter.
func (s *Service) ListThreads(ctx context.Context, filter domain.ThreadFilter) ([]domain.Thread, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("threads.list", err)
	}
	repo := s.resolveRepo(filter.Repository)
	if err := requireRepo("threads.list", repo); err != nil {
		return nil, err
	}
	if filter.Status != "" {
		if err := filter.Status.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"threads.list",
				err.Error(),
				"Use --status active, --status resolved, or --status stale.",
				err,
			)
		}
	}
	if s.adapters.Store == nil {
		return nil, storageNotConfiguredError("threads.list")
	}
	filter.Repository = repo
	threads, err := s.adapters.Store.ListThreads(ctx, filter)
	if err != nil {
		return nil, domain.AsError("threads.list", err)
	}
	return threads, nil
}

// GetThread returns a thread together with its member items and relationships.
func (s *Service) GetThread(ctx context.Context, id domain.ThreadID) (domain.ThreadDetail, error) {
	if err := ctx.Err(); err != nil {
		return domain.ThreadDetail{}, domain.AsError("threads.show", err)
	}
	if strings.TrimSpace(string(id)) == "" {
		return domain.ThreadDetail{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"threads.show",
			"Thread ID (--id) is required.",
			"Pass a non-empty thread ID via --id <thread-id>.",
			nil,
		)
	}
	if s.adapters.Store == nil {
		return domain.ThreadDetail{}, storageNotConfiguredError("threads.show")
	}
	detail, err := s.adapters.Store.GetThreadDetail(ctx, id)
	if err != nil {
		return domain.ThreadDetail{}, domain.AsError("threads.show", err)
	}
	return detail, nil
}

// ListRelationships returns relationships matching filter.
func (s *Service) ListRelationships(ctx context.Context, filter domain.RelationshipFilter) ([]domain.Relationship, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("relationships.list", err)
	}
	repo := s.resolveRepo(filter.Repository)
	if err := requireRepo("relationships.list", repo); err != nil {
		return nil, err
	}
	if filter.ItemNumber < 0 {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.list",
			"Item number (--item) must not be negative.",
			"Pass a positive GitHub item number via --item <number>.",
			nil,
		)
	}
	if filter.Type != "" {
		if err := filter.Type.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.list",
				err.Error(),
				"Pass a valid relationship type: reports_same_problem, proposes_fix, implements_decision, blocks_or_depends, related_context, or duplicate_of.",
				err,
			)
		}
	}
	if s.adapters.Store == nil {
		return nil, storageNotConfiguredError("relationships.list")
	}
	filter.Repository = repo
	rels, err := s.adapters.Store.ListRelationships(ctx, filter)
	if err != nil {
		return nil, domain.AsError("relationships.list", err)
	}
	return rels, nil
}

// GetRelationship returns a single relationship and its supporting evidence.
func (s *Service) GetRelationship(ctx context.Context, id domain.RelationshipID) (domain.Relationship, error) {
	if err := ctx.Err(); err != nil {
		return domain.Relationship{}, domain.AsError("relationships.show", err)
	}
	if strings.TrimSpace(string(id)) == "" {
		return domain.Relationship{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.show",
			"Relationship ID (--id) is required.",
			"Pass a non-empty relationship ID via --id <relationship-id>.",
			nil,
		)
	}
	if s.adapters.Store == nil {
		return domain.Relationship{}, storageNotConfiguredError("relationships.show")
	}
	rel, err := s.adapters.Store.GetRelationship(ctx, id)
	if err != nil {
		return domain.Relationship{}, domain.AsError("relationships.show", err)
	}
	return rel, nil
}

// VerifyRelationship sets human verification status on a relationship.
func (s *Service) VerifyRelationship(ctx context.Context, id domain.RelationshipID, verified bool) (domain.Relationship, error) {
	if err := ctx.Err(); err != nil {
		return domain.Relationship{}, domain.AsError("relationships.verify", err)
	}
	if strings.TrimSpace(string(id)) == "" {
		return domain.Relationship{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.verify",
			"Relationship ID (--id) is required.",
			"Pass a non-empty relationship ID via --id <relationship-id>.",
			nil,
		)
	}
	if s.adapters.Store == nil {
		return domain.Relationship{}, storageNotConfiguredError("relationships.verify")
	}
	rel, err := s.adapters.Store.VerifyRelationship(ctx, id, verified)
	if err != nil {
		return domain.Relationship{}, domain.AsError("relationships.verify", err)
	}
	return rel, nil
}

// Stats computes repository activity and graph statistics from committed storage state.
func (s *Service) Stats(ctx context.Context, repoOverride string) (domain.RepoStats, error) {
	if err := ctx.Err(); err != nil {
		return domain.RepoStats{}, domain.AsError("stats", err)
	}
	repo := s.resolveRepo(repoOverride)
	if err := requireRepo("stats", repo); err != nil {
		return domain.RepoStats{}, err
	}
	if s.adapters.Store == nil {
		return domain.RepoStats{}, storageNotConfiguredError("stats")
	}
	stats, err := s.adapters.Store.GetRepoStats(ctx, repo)
	if err != nil {
		return domain.RepoStats{}, domain.AsError("stats", err)
	}
	return stats, nil
}

func (s *Service) resolveRepo(override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(s.cfg.Repository)
}

func requireRepo(operation, repo string) error {
	if strings.TrimSpace(repo) == "" {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			"Repository is required for this operation.",
			"Pass --repo <owner/name> or set AUTOMAIN_REPO=<owner/name>.",
			nil,
		)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
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

func storageNotConfiguredError(operation string) error {
	return domain.NewError(
		domain.ErrorCodeNotConfigured,
		operation,
		"SQLite storage adapter is not wired yet.",
		"Run 'automain status' to inspect adapter availability; SQLite persistence is scheduled in WO2.",
		nil,
	)
}
