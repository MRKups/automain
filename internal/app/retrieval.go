package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

// PrepareIndex only copies/chunks source text into the metadata-only catalog.
func (s *Service) PrepareIndex(ctx context.Context, repo string) (domain.IndexResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.retrieval.Prepare(ctx, s.resolveRepo(repo))
	if err != nil {
		return result, domain.AsError("index.prepare", err)
	}
	return result, nil
}

func (s *Service) IndexStatus(ctx context.Context, repo string) (domain.IndexStatus, error) {
	result, err := s.retrieval.Status(ctx, s.resolveRepo(repo))
	if err != nil {
		return result, domain.AsError("index.status", err)
	}
	return result, nil
}

// EmbedIndex prepares source passages and embeds pending work with the injected provider.
// It does not run relationship analysis, even when LLM credentials are configured.
func (s *Service) EmbedIndex(ctx context.Context, repo string, limit int) (domain.IndexResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.retrieval.Embed(ctx, s.resolveRepo(repo), limit)
	if err != nil {
		return result, domain.AsError("index.embed", err)
	}
	return result, nil
}

func (s *Service) Search(ctx context.Context, opts domain.SearchOptions) (domain.SearchResult, error) {
	opts.Repository = s.resolveRepo(opts.Repository)
	result, err := s.retrieval.Search(ctx, opts)
	if err != nil {
		return result, domain.AsError("search", err)
	}
	return result, nil
}

func (s *Service) Similar(ctx context.Context, opts domain.SimilarOptions) (domain.SimilarResult, error) {
	opts.Repository = s.resolveRepo(opts.Repository)
	result, err := s.retrieval.Similar(ctx, opts)
	return result, wrapRetrievalError("similar", err)
}

func (s *Service) ReconcileIndex(ctx context.Context, repo string) (domain.IndexResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.retrieval.Reconcile(ctx, s.resolveRepo(repo))
	return result, wrapRetrievalError("index.reconcile", err)
}

func (s *Service) MigrateIndex(ctx context.Context, repo, path string, limit int) (result domain.MigrationResult, retErr error) {
	result.Repository = s.resolveRepo(repo)
	if err := requireRepo("index.migrate", result.Repository); err != nil {
		return result, err
	}
	if limit < 1 || limit > 10000 {
		return result, domain.NewError(domain.ErrorCodeInvalidInput, "index.migrate", "Migration limit must be between 1 and 10000 vector writes.", "Use a bounded positive limit, such as 100.", nil)
	}
	if s.adapters.OpenLegacy == nil {
		return result, domain.NewError(domain.ErrorCodeNotConfigured, "index.migrate", "The read-only legacy index reader is not wired.", "Inspect the application build and migration configuration.", nil)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return result, domain.NewError(domain.ErrorCodeInvalidInput, "index.migrate", "An explicit legacy source path is required.", "Pass --from with a preserved vectors.db file; it will be opened read-only.", nil)
	}
	// Check aliases before the reader opens any file. It must never be a source
	// or metadata-catalog handle, including through links to an existing file.
	paths := s.cfg
	paths.VectorDBPath = path
	if err := paths.ValidateDatabasePaths(); err != nil {
		return result, domain.NewError(domain.ErrorCodeInvalidInput, "index.migrate", err.Error(), "Use distinct canonical, catalog, and preserved legacy vector files.", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	legacy, err := s.adapters.OpenLegacy(ctx, path)
	if err != nil {
		return result, domain.AsError("index.migrate", err)
	}
	defer func() {
		if err := legacy.Close(); err != nil {
			retErr = errors.Join(retErr, domain.AsError("index.migrate", err))
		}
	}()
	result, err = s.retrieval.Migrate(ctx, result.Repository, legacy, limit)
	return result, wrapRetrievalError("index.migrate", err)
}

// Readiness probes local persistence and Qdrant, never provider credentials.
// Liveness remains a separate transport endpoint.
func (s *Service) Readiness(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if s.adapters.Store == nil || s.adapters.Catalog == nil || s.adapters.Vectors == nil {
		return domain.NewError(domain.ErrorCodeNotConfigured, "ready", "Canonical storage, the passage catalog, and Qdrant must be wired for readiness.", "Inspect local paths and Qdrant configuration with status.", nil)
	}
	if _, err := s.adapters.Store.ListRepositories(ctx); err != nil {
		return domain.AsError("ready", err)
	}
	if err := s.adapters.Catalog.Check(ctx); err != nil {
		return domain.AsError("ready", err)
	}
	return wrapRetrievalError("ready", s.adapters.Vectors.Check(ctx))
}

func wrapRetrievalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return domain.AsError(operation, err)
}
