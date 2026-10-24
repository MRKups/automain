package app

import (
	"context"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/analysis"
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
	Version       string        `json:"version"`
	SchemaVersion int           `json:"schema_version"`
	Config        StatusConfig  `json:"config"`
	Adapters      AdapterStatus `json:"adapters"`
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
	schemaVersion := 0
	if s.adapters.Store != nil {
		schemaVersion = s.adapters.Store.SchemaVersion()
	}
	return StatusReport{
		Version:       Version,
		SchemaVersion: schemaVersion,
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
	if s.adapters.Store == nil {
		return domain.SyncResult{}, storageNotConfiguredError("sync")
	}
	if s.adapters.GitHub == nil {
		return domain.SyncResult{}, domain.NewError(
			domain.ErrorCodeNotConfigured,
			"sync",
			"GitHub ingestion adapter is not wired yet.",
			"Run 'automain status' to inspect adapter availability; GitHub ingestion is scheduled in WO3.",
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

	syncedAt := time.Now().UTC().Truncate(time.Second)
	batch, err := s.adapters.GitHub.FetchUpdates(ctx, opts, since)
	if err != nil {
		return domain.SyncResult{}, domain.AsError("sync", err)
	}
	res, err := s.adapters.Store.UpsertItemsBatch(ctx, repo, batch, syncedAt)
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
	if s.adapters.Store == nil {
		return domain.AnalyzeResult{}, storageNotConfiguredError("analyze")
	}
	if s.adapters.Finder == nil || s.adapters.Analyser == nil {
		return domain.AnalyzeResult{}, domain.NewError(
			domain.ErrorCodeNotConfigured,
			"analyze",
			"Candidate finder and relationship analyser adapters are not wired yet.",
			"Run 'automain status' to inspect adapter availability; discovery and analysis adapters are scheduled in WO4.",
			nil,
		)
	}

	type configValidator interface {
		ValidateConfig() error
	}
	if v, ok := s.adapters.Analyser.(configValidator); ok {
		if err := v.ValidateConfig(); err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}
	}
	if v, ok := s.adapters.Finder.(configValidator); ok {
		if err := v.ValidateConfig(); err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}
	}

	var targetItems []domain.Item
	if opts.ItemNumber > 0 {
		item, err := s.adapters.Store.GetItem(ctx, repo, opts.ItemNumber)
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}
		if opts.Reanalyze || item.IsDirty() {
			targetItems = []domain.Item{item}
		}
	} else if opts.Reanalyze {
		items, err := s.adapters.Store.ListItems(ctx, domain.ItemFilter{
			Repository: repo,
			DirtyOnly:  false,
			Limit:      opts.Limit,
		})
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}
		targetItems = items
	} else {
		items, err := s.adapters.Store.ListDirtyItems(ctx, repo, opts.Limit)
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}
		targetItems = items
	}

	result := domain.AnalyzeResult{
		Repository: repo,
	}

	for _, target := range targetItems {
		if err := ctx.Err(); err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}

		item, comments, err := s.adapters.Store.GetItemWithComments(ctx, repo, target.Number)
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}

		existingThreads, err := s.adapters.Store.ListThreads(ctx, domain.ThreadFilter{
			Repository: repo,
		})
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}

		summaries := make([]domain.ThreadSummary, 0, len(existingThreads))
		existingByID := make(map[domain.ThreadID]domain.Thread, len(existingThreads))
		existingByNormTitle := make(map[string]domain.Thread, len(existingThreads))
		for _, t := range existingThreads {
			summaries = append(summaries, domain.ThreadSummary{
				ID:        t.ID,
				Title:     t.Title,
				Summary:   t.Summary,
				Status:    t.Status,
				ItemCount: t.ItemCount,
			})
			existingByID[t.ID] = t
			normTitle := strings.ToLower(strings.Join(strings.Fields(t.Title), " "))
			if normTitle != "" {
				existingByNormTitle[normTitle] = t
			}
		}

		var candidates []domain.CandidateThread
		detailItemsByID := make(map[domain.ThreadID][]domain.Item)

		if len(summaries) > 0 {
			candidateIDs, err := s.adapters.Finder.FindCandidates(ctx, repo, item, summaries)
			if err != nil {
				return domain.AnalyzeResult{}, domain.AsError("analyze", err)
			}
			seenCand := make(map[domain.ThreadID]struct{}, len(candidateIDs))
			for _, rawID := range candidateIDs {
				cid := domain.ThreadID(strings.TrimSpace(string(rawID)))
				if cid == "" {
					continue
				}
				if _, dup := seenCand[cid]; dup {
					continue
				}
				if _, exists := existingByID[cid]; !exists {
					continue
				}
				seenCand[cid] = struct{}{}
				detail, err := s.adapters.Store.GetThreadDetail(ctx, cid)
				if err != nil {
					return domain.AnalyzeResult{}, domain.AsError("analyze", err)
				}
				candidates = append(candidates, domain.CandidateThread{
					Thread: detail.Thread,
					Items:  detail.Items,
				})
				detailItemsByID[cid] = detail.Items
			}
		}

		rawDecision, err := s.adapters.Analyser.Analyse(ctx, item, comments, candidates)
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}

		decision, err := analysis.NormalizeDecision(item, comments, candidates, rawDecision)
		if err != nil {
			return domain.AnalyzeResult{}, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"analyze",
				err.Error(),
				"Ensure relationship analysis output contains valid thread IDs, relationship types, confidence levels, and evidence.",
				err,
			)
		}

		record, err := s.buildItemAnalysisRecord(ctx, repo, item, comments, existingByID, existingByNormTitle, detailItemsByID, decision)
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}

		if err := s.adapters.Store.SaveItemAnalysis(ctx, record); err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}

		result.AnalyzedItems++
		result.ThreadsCreated += len(record.ThreadsToCreate)
		result.ThreadsUpdated += len(record.ThreadsToUpdate)
		result.RelationshipsUpserted += len(record.Relationships)
	}

	remainingDirty, err := s.adapters.Store.ListDirtyItems(ctx, repo, 0)
	if err != nil {
		return domain.AnalyzeResult{}, domain.AsError("analyze", err)
	}
	result.RemainingDirtyItems = len(remainingDirty)

	return result, nil
}

func (s *Service) buildItemAnalysisRecord(
	ctx context.Context,
	repo string,
	item domain.Item,
	comments []domain.Comment,
	existingByID map[domain.ThreadID]domain.Thread,
	existingByNormTitle map[string]domain.Thread,
	detailItemsByID map[domain.ThreadID][]domain.Item,
	decision domain.AnalysisDecision,
) (domain.ItemAnalysisRecord, error) {
	now := time.Now().UTC().Truncate(time.Second)
	record := domain.ItemAnalysisRecord{
		Repository:   repo,
		ItemNumber:   item.Number,
		AnalyzedHash: item.ContentHash,
	}

	updatedIdxByID := make(map[domain.ThreadID]int)
	createdIdxByID := make(map[domain.ThreadID]int)
	seenRelThread := make(map[domain.ThreadID]struct{})

	addRelationship := func(threadID domain.ThreadID, relType domain.RelationshipType, conf domain.ConfidenceLevel, explanation string, evidence []domain.EvidenceRef, memberItems []domain.Item) {
		if _, exists := seenRelThread[threadID]; exists {
			return
		}
		seenRelThread[threadID] = struct{}{}

		explicit := analysis.DetectExplicitLink(repo, item, comments, memberItems, evidence)
		rel := domain.Relationship{
			ID:               analysis.DeterministicRelationshipID(repo, threadID, item.Number, relType),
			Repository:       repo,
			ThreadID:         threadID,
			ItemNumber:       item.Number,
			Type:             relType,
			Explanation:      explanation,
			Confidence:       conf,
			Evidence:         evidence,
			ExplicitlyLinked: explicit,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		record.Relationships = append(record.Relationships, rel)
	}

	for _, m := range decision.MatchedThreads {
		existing, ok := existingByID[m.ThreadID]
		if !ok {
			detail, err := s.adapters.Store.GetThreadDetail(ctx, m.ThreadID)
			if err != nil {
				return domain.ItemAnalysisRecord{}, err
			}
			existing = detail.Thread
			existingByID[m.ThreadID] = existing
			normTitle := strings.ToLower(strings.Join(strings.Fields(existing.Title), " "))
			if normTitle != "" {
				existingByNormTitle[normTitle] = existing
			}
			detailItemsByID[m.ThreadID] = detail.Items
		}

		memberItems, hasItems := detailItemsByID[existing.ID]
		if !hasItems {
			if detail, err := s.adapters.Store.GetThreadDetail(ctx, existing.ID); err == nil {
				memberItems = detail.Items
				detailItemsByID[existing.ID] = memberItems
			}
		}

		if idx, alreadyUpdated := updatedIdxByID[existing.ID]; alreadyUpdated {
			if strings.TrimSpace(m.UpdatedThreadSummary) != "" {
				record.ThreadsToUpdate[idx].Summary = m.UpdatedThreadSummary
			}
			if m.UpdatedThreadStatus != "" {
				record.ThreadsToUpdate[idx].Status = m.UpdatedThreadStatus
			}
		} else {
			updatedIdxByID[existing.ID] = len(record.ThreadsToUpdate)
			record.ThreadsToUpdate = append(record.ThreadsToUpdate, domain.Thread{
				ID:         existing.ID,
				Repository: repo,
				Title:      existing.Title,
				Summary:    m.UpdatedThreadSummary,
				Status:     m.UpdatedThreadStatus,
				Revision:   existing.Revision + 1,
				CreatedAt:  existing.CreatedAt,
				UpdatedAt:  now,
			})
		}

		addRelationship(existing.ID, m.RelationshipType, m.Confidence, m.Explanation, m.Evidence, memberItems)
	}

	for _, n := range decision.NewThreads {
		normTitle := strings.ToLower(strings.Join(strings.Fields(n.Title), " "))
		tid := analysis.DeterministicThreadID(repo, n.Title)

		existing, existsInStore := existingByID[tid]
		if !existsInStore && normTitle != "" {
			if byTitle, ok := existingByNormTitle[normTitle]; ok {
				existing = byTitle
				tid = existing.ID
				existsInStore = true
			}
		}

		if existsInStore {
			memberItems, hasItems := detailItemsByID[existing.ID]
			if !hasItems {
				if detail, err := s.adapters.Store.GetThreadDetail(ctx, existing.ID); err == nil {
					memberItems = detail.Items
					detailItemsByID[existing.ID] = memberItems
				}
			}
			if _, alreadyUpdated := updatedIdxByID[existing.ID]; !alreadyUpdated {
				updatedIdxByID[existing.ID] = len(record.ThreadsToUpdate)
				record.ThreadsToUpdate = append(record.ThreadsToUpdate, domain.Thread{
					ID:         existing.ID,
					Repository: repo,
					Title:      existing.Title,
					Summary:    n.Summary,
					Status:     n.Status,
					Revision:   existing.Revision + 1,
					CreatedAt:  existing.CreatedAt,
					UpdatedAt:  now,
				})
			}
			addRelationship(existing.ID, n.RelationshipType, n.Confidence, n.Explanation, n.Evidence, memberItems)
			continue
		}

		if _, alreadyCreating := createdIdxByID[tid]; !alreadyCreating {
			createdIdxByID[tid] = len(record.ThreadsToCreate)
			record.ThreadsToCreate = append(record.ThreadsToCreate, domain.Thread{
				ID:         tid,
				Repository: repo,
				Title:      n.Title,
				Summary:    n.Summary,
				Status:     n.Status,
				Revision:   1,
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}

		addRelationship(tid, n.RelationshipType, n.Confidence, n.Explanation, n.Evidence, nil)
	}

	return record, nil
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
