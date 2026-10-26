package app

import (
	"context"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/analysis"
	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
	"github.com/MRKups/automain/internal/retrieval"
)

const Version = "0.1.0-dev"

// Adapters groups the external effect ports injected into the application Service.
type Adapters struct {
	Store      domain.Store
	GitHub     domain.GitHubSource
	Finder     domain.CandidateFinder
	Analyser   domain.RelationshipAnalyser
	EmbeddedUI bool
	Catalog    domain.ChunkCatalog
	Vectors    domain.VectorStore
	Embedder   domain.EmbeddingProvider
	Decider    domain.DecisionProvider
	OpenLegacy domain.LegacyIndexOpener
}

// StatusConfig is a safe, secret-redacted projection of runtime configuration.
type StatusConfig struct {
	DBPath                 string `json:"db_path"`
	VectorDBPath           string `json:"vector_db_path"`
	IndexDBPath            string `json:"index_db_path"`
	QdrantHost             string `json:"qdrant_host"`
	QdrantPort             int    `json:"qdrant_port"`
	QdrantTLS              bool   `json:"qdrant_tls"`
	QdrantCollectionPrefix string `json:"qdrant_collection_prefix"`
	QdrantAPIKeySet        bool   `json:"qdrant_api_key_set"`
	Repository             string `json:"repository,omitempty"`
	Finder                 string `json:"finder"`
	ListenAddr             string `json:"listen_addr"`
	Timeout                string `json:"timeout"`
	GitHubTokenSet         bool   `json:"github_token_set"`
	LLMProvider            string `json:"llm_provider"`
	LLMAPIKeySet           bool   `json:"llm_api_key_set"`
	LLMModel               string `json:"llm_model"`
	LLMBaseURL             string `json:"llm_base_url,omitempty"`
	EmbeddingProvider      string `json:"embedding_provider"`
	EmbeddingModel         string `json:"embedding_model"`
	EmbeddingBaseURL       string `json:"embedding_base_url"`
	EmbeddingDimensions    int    `json:"embedding_dimensions"`
	EmbeddingAPIKeySet     bool   `json:"embedding_api_key_set"`
	DecisionProvider       string `json:"decision_provider"`
	DecisionModel          string `json:"decision_model"`
	DecisionEndpoint       string `json:"decision_endpoint"`
	DecisionAPIKeySet      bool   `json:"decision_api_key_set"`
}

// AdapterStatus reports which effect ports are wired in the current binary composition.
type AdapterStatus struct {
	Storage              bool `json:"storage"`
	GitHub               bool `json:"github"`
	CandidateFinder      bool `json:"candidate_finder"`
	RelationshipAnalyser bool `json:"relationship_analyser"`
	EmbeddedUI           bool `json:"embedded_ui"`
	VectorIndex          bool `json:"vector_index"`
	ChunkCatalog         bool `json:"chunk_catalog"`
	VectorStore          bool `json:"vector_store"`
	EmbeddingProvider    bool `json:"embedding_provider"`
	DecisionProvider     bool `json:"decision_provider"`
}

// StatusReport summarizes application readiness, configuration, and adapter availability.
type StatusReport struct {
	Version             string        `json:"version"`
	SchemaVersion       int           `json:"schema_version"`
	VectorSchemaVersion int           `json:"vector_schema_version"`
	IndexSchemaVersion  int           `json:"index_schema_version"`
	Config              StatusConfig  `json:"config"`
	Adapters            AdapterStatus `json:"adapters"`
}

// Service coordinates domain operations across the CLI and HTTP API surfaces.
type Service struct {
	mutations chan struct{}
	cfg       config.Config
	adapters  Adapters
	retrieval *retrieval.Service
}

// NewService constructs the shared application service facade.
func NewService(cfg config.Config, adapters Adapters) *Service {
	return &Service{
		mutations: make(chan struct{}, 1),
		cfg:       cfg,
		adapters:  adapters,
		retrieval: retrieval.New(adapters.Store, adapters.Catalog, adapters.Vectors, adapters.Embedder),
	}
}

// One mutation owns the service through its remote effects. Waiting callers can
// leave on cancellation without taking ownership or starting downstream work.
func (s *Service) acquireMutation(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return mutationCanceled(operation, err)
	}
	if s.mutations == nil {
		return domain.NewError(domain.ErrorCodeNotConfigured, operation,
			"Mutation execution is not initialized.", "Construct the application service before invoking mutations.", nil)
	}
	select {
	case s.mutations <- struct{}{}:
		// The slot and cancellation can become ready together. Do not start an
		// operation whose context expired while ownership was being granted.
		if err := ctx.Err(); err != nil {
			s.releaseMutation()
			return mutationCanceled(operation, err)
		}
		return nil
	case <-ctx.Done():
		return mutationCanceled(operation, ctx.Err())
	}
}

func (s *Service) releaseMutation() { <-s.mutations }

func mutationCanceled(operation string, cause error) error {
	return domain.NewError(domain.ErrorCodeExternalUnavailable, operation,
		"Operation was canceled or timed out before mutation execution.",
		"Retry explicitly if still needed; no queued mutation was started.", cause)
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
	vectorVersion := 0
	if s.adapters.Catalog != nil {
		vectorVersion = s.adapters.Catalog.SchemaVersion()
	}
	return StatusReport{
		Version:             Version,
		SchemaVersion:       schemaVersion,
		VectorSchemaVersion: vectorVersion,
		IndexSchemaVersion:  vectorVersion,
		Config: StatusConfig{
			DBPath:                 s.cfg.DBPath,
			VectorDBPath:           s.cfg.VectorPath(),
			IndexDBPath:            s.cfg.IndexPath(),
			QdrantHost:             s.cfg.QdrantHost,
			QdrantPort:             s.cfg.QdrantPort,
			QdrantTLS:              s.cfg.QdrantTLS,
			QdrantCollectionPrefix: s.cfg.QdrantCollectionPrefix,
			QdrantAPIKeySet:        s.cfg.QdrantAPIKeySet(),
			Repository:             s.cfg.Repository,
			Finder:                 s.cfg.Finder,
			ListenAddr:             s.cfg.ListenAddr,
			Timeout:                s.cfg.Timeout.String(),
			GitHubTokenSet:         s.cfg.GitHubTokenSet(),
			LLMProvider:            config.NormalizeLLMProvider(s.cfg.LLMProvider),
			LLMAPIKeySet:           s.cfg.LLMAPIKeySet(),
			LLMModel:               s.cfg.LLMModel,
			LLMBaseURL:             s.cfg.LLMBaseURL,
			EmbeddingProvider:      config.EmbeddingProvider,
			EmbeddingModel:         config.EmbeddingModel,
			EmbeddingBaseURL:       s.cfg.EmbeddingURL(),
			EmbeddingDimensions:    s.cfg.EmbeddingSize(),
			EmbeddingAPIKeySet:     s.cfg.EmbeddingAPIKeySet(),
			DecisionProvider:       config.DecisionProvider,
			DecisionModel:          s.cfg.DecisionModelName(),
			DecisionEndpoint:       s.cfg.DecisionURL(),
			DecisionAPIKeySet:      s.cfg.DecisionAPIKeySet(),
		},
		Adapters: AdapterStatus{
			Storage:              s.adapters.Store != nil,
			GitHub:               s.adapters.GitHub != nil,
			CandidateFinder:      s.adapters.Finder != nil,
			RelationshipAnalyser: s.adapters.Analyser != nil,
			EmbeddedUI:           s.adapters.EmbeddedUI,
			VectorIndex:          s.adapters.Catalog != nil && s.adapters.Vectors != nil,
			ChunkCatalog:         s.adapters.Catalog != nil,
			VectorStore:          s.adapters.Vectors != nil,
			EmbeddingProvider:    s.adapters.Embedder != nil,
			DecisionProvider:     s.adapters.Decider != nil,
		},
	}, nil
}

// Sync imports repository issues, pull requests, discussions, and comments.
func (s *Service) Sync(ctx context.Context, opts domain.SyncOptions) (domain.SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.SyncResult{}, mutationCanceled("sync", err)
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

	if err := s.acquireMutation(ctx, "sync"); err != nil {
		return domain.SyncResult{}, err
	}
	defer s.releaseMutation()

	opts.Repository = repo
	if !opts.Full {
		if _, err := s.adapters.Store.GetSyncWatermark(ctx, repo); err != nil {
			return domain.SyncResult{}, domain.AsError("sync", err)
		}
	}
	var since *time.Time
	if !opts.Full && opts.Since != nil {
		since = opts.Since
	}

	batch, err := s.adapters.GitHub.FetchUpdates(ctx, opts, since)
	if err != nil {
		return domain.SyncResult{}, domain.AsError("sync", err)
	}
	syncedAt := time.Now().UTC().Truncate(time.Second)
	res, err := s.adapters.Store.UpsertItemsBatch(ctx, repo, batch, syncedAt)
	if err != nil {
		return domain.SyncResult{}, domain.AsError("sync", err)
	}
	return res, nil
}

// Analyze classifies dirty items, discovers candidate threads, and persists verified relationships.
func (s *Service) Analyze(ctx context.Context, opts domain.AnalyzeOptions) (domain.AnalyzeResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.AnalyzeResult{}, mutationCanceled("analyze", err)
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
	if err := s.acquireMutation(ctx, "analyze"); err != nil {
		return domain.AnalyzeResult{}, err
	}
	defer s.releaseMutation()

	activeFinder := s.adapters.Finder
	if overrideFinder := strings.ToLower(strings.TrimSpace(opts.Finder)); overrideFinder != "" {
		llmClient, _ := s.adapters.Analyser.(*analysis.Client)
		f, err := analysis.NewFinder(overrideFinder, llmClient)
		if err != nil {
			return domain.AnalyzeResult{}, domain.AsError("analyze", err)
		}
		if overrideFinder != strings.ToLower(strings.TrimSpace(s.cfg.Finder)) || activeFinder == nil {
			activeFinder = f
		}
	}

	if activeFinder == nil || s.adapters.Analyser == nil {
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
	if v, ok := activeFinder.(configValidator); ok {
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
			candidateIDs, err := activeFinder.FindCandidates(ctx, repo, item, summaries)
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
		return domain.Relationship{}, mutationCanceled("relationships.verify", err)
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
	if err := s.acquireMutation(ctx, "relationships.verify"); err != nil {
		return domain.Relationship{}, err
	}
	defer s.releaseMutation()
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

// Graph returns the full repository thread-item relationship network and analytics view.
func (s *Service) Graph(ctx context.Context, repoOverride string) (domain.GraphView, error) {
	if err := ctx.Err(); err != nil {
		return domain.GraphView{}, domain.AsError("graph", err)
	}
	repo := s.resolveRepo(repoOverride)
	if err := requireRepo("graph", repo); err != nil {
		return domain.GraphView{}, err
	}
	if s.adapters.Store == nil {
		return domain.GraphView{}, storageNotConfiguredError("graph")
	}
	graph, err := s.adapters.Store.GetGraph(ctx, repo)
	if err != nil {
		return domain.GraphView{}, domain.AsError("graph", err)
	}
	return graph, nil
}

// ListItems returns repository items matching filter.
func (s *Service) ListItems(ctx context.Context, filter domain.ItemFilter) ([]domain.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("items.list", err)
	}
	repo := s.resolveRepo(filter.Repository)
	if err := requireRepo("items.list", repo); err != nil {
		return nil, err
	}
	if filter.Kind != "" {
		if err := filter.Kind.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.list",
				err.Error(),
				"Use kind issue, pull_request, or discussion.",
				err,
			)
		}
	}
	if filter.State != "" {
		if err := filter.State.Validate(); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.list",
				err.Error(),
				"Use state open, closed, merged, or answered.",
				err,
			)
		}
	}
	if filter.Limit < 0 {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items.list",
			"Item limit must not be negative.",
			"Pass a non-negative integer for limit.",
			nil,
		)
	}
	if s.adapters.Store == nil {
		return nil, storageNotConfiguredError("items.list")
	}
	filter.Repository = repo
	items, err := s.adapters.Store.ListItems(ctx, filter)
	if err != nil {
		return nil, domain.AsError("items.list", err)
	}
	return items, nil
}

// GetItem returns a repository item together with its chronologically ordered comments.
func (s *Service) GetItem(ctx context.Context, repoOverride string, number int) (domain.Item, []domain.Comment, error) {
	if err := ctx.Err(); err != nil {
		return domain.Item{}, nil, domain.AsError("items.show", err)
	}
	repo := s.resolveRepo(repoOverride)
	if err := requireRepo("items.show", repo); err != nil {
		return domain.Item{}, nil, err
	}
	if number <= 0 {
		return domain.Item{}, nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items.show",
			"Item number must be a positive integer.",
			"Pass a positive GitHub item number.",
			nil,
		)
	}
	if s.adapters.Store == nil {
		return domain.Item{}, nil, storageNotConfiguredError("items.show")
	}
	item, comments, err := s.adapters.Store.GetItemWithComments(ctx, repo, number)
	if err != nil {
		return domain.Item{}, nil, domain.AsError("items.show", err)
	}
	return item, comments, nil
}

// ListRepositories returns sync watermarks for repositories stored in SQLite.
func (s *Service) ListRepositories(ctx context.Context) ([]domain.SyncWatermark, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("repositories.list", err)
	}
	if s.adapters.Store == nil {
		return nil, storageNotConfiguredError("repositories.list")
	}
	repos, err := s.adapters.Store.ListRepositories(ctx)
	if err != nil {
		return nil, domain.AsError("repositories.list", err)
	}
	cfgRepo := strings.TrimSpace(s.cfg.Repository)
	if cfgRepo != "" && domain.ValidateRepoSlug(cfgRepo) == nil {
		found := false
		for _, r := range repos {
			if r.Repository == cfgRepo {
				found = true
				break
			}
		}
		if !found {
			wm, wmErr := s.adapters.Store.GetSyncWatermark(ctx, cfgRepo)
			if wmErr == nil {
				repos = append(repos, wm)
			}
		}
	}
	if repos == nil {
		repos = []domain.SyncWatermark{}
	}
	return repos, nil
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
