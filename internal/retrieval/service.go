package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MRKups/automain/internal/domain"
)

// Sources is read-only. Retrieval cannot mutate authoritative items or decisions.
type Sources interface {
	ListItems(ctx context.Context, filter domain.ItemFilter) ([]domain.Item, error)
	GetItemWithComments(ctx context.Context, repo string, number int) (domain.Item, []domain.Comment, error)
}

// Service separates source preparation, embedding, and retrieval from downstream
// relevance/relationship evaluation. It never invokes RelationshipAnalyser.
type Service struct {
	sources  Sources
	catalog  domain.ChunkCatalog
	vectors  domain.VectorStore
	embedder domain.EmbeddingProvider
}

func New(sources Sources, catalog domain.ChunkCatalog, vectors domain.VectorStore, embedder domain.EmbeddingProvider) *Service {
	return &Service{sources: sources, catalog: catalog, vectors: vectors, embedder: embedder}
}

// Prepare rebuilds source passages without an embedding API or generative model.
// Full-repository reconciliation avoids deleting chunks due to a partial limit.
func (s *Service) Prepare(ctx context.Context, repo string) (domain.IndexResult, error) {
	result := domain.IndexResult{Repository: repo}
	if err := s.validate(repo); err != nil {
		return result, err
	}
	items, err := s.sources.ListItems(ctx, domain.ItemFilter{Repository: repo})
	if err != nil {
		return result, err
	}
	chunks := []domain.SearchChunk{}
	for _, listed := range items {
		item, comments, err := s.sources.GetItemWithComments(ctx, repo, listed.Number)
		if err != nil {
			// Do not replace the index with an incomplete source snapshot.
			return result, err
		}
		parts, err := Chunks(item, comments)
		if err != nil {
			return result, fmt.Errorf("prepare %s#%d: %w", repo, item.Number, err)
		}
		chunks = append(chunks, parts...)
	}
	if err := s.catalog.ReplaceChunks(ctx, repo, chunks); err != nil {
		return result, err
	}
	result.PreparedItems = len(items)
	result.PreparedChunks = len(chunks)
	return result, nil
}

func (s *Service) Status(ctx context.Context, repo string) (domain.IndexStatus, error) {
	if err := s.validate(repo); err != nil {
		return domain.IndexStatus{}, err
	}
	return s.catalog.IndexStatus(ctx, repo)
}

// Embed uses the injected provider without a synthetic vector fallback or hidden
// LLM analysis call. Limit counts chunks, not items.
func (s *Service) Embed(ctx context.Context, repo string, limit int) (domain.IndexResult, error) {
	result := domain.IndexResult{Repository: repo}
	if err := s.validate(repo); err != nil {
		return result, err
	}
	if limit <= 0 || limit > 10000 {
		return result, invalid("embedding chunk limit must be between 1 and 10000")
	}
	if err := s.requireVectors(); err != nil {
		return result, err
	}
	space, err := s.space()
	if err != nil {
		return result, err
	}
	result, err = s.Prepare(ctx, repo)
	if err != nil {
		return result, err
	}
	if err := s.catalog.RegisterSpace(ctx, space); err != nil {
		return result, err
	}
	if err := s.reconcile(ctx, repo, &result); err != nil {
		return result, err
	}
	for result.EmbeddedChunks+result.ReusedChunks < limit {
		pending, err := s.catalog.PendingChunks(ctx, repo, space, min(32, limit-result.EmbeddedChunks-result.ReusedChunks))
		if err != nil {
			return result, err
		}
		if len(pending) == 0 {
			break
		}
		present, err := s.vectors.Existing(ctx, repo, space, pending)
		if err != nil {
			return result, err
		}
		var reused, missing []domain.SearchChunk
		for _, chunk := range pending {
			if present[chunk.ID] {
				reused = append(reused, chunk)
			} else {
				missing = append(missing, chunk)
			}
		}
		if len(reused) > 0 {
			if err := s.catalog.TrackChunks(ctx, repo, space, reused); err != nil {
				return result, err
			}
			if err := s.confirmCurrent(ctx, repo, space, reused); err != nil {
				return result, err
			}
			result.ReusedChunks += len(reused)
		}
		if len(missing) == 0 {
			continue
		}
		texts := make([]string, len(missing))
		for i, c := range missing {
			texts[i] = c.InputText()
		}
		vectors, err := s.embedder.Embed(ctx, domain.EmbeddingDocument, texts)
		if err != nil {
			return result, err
		}
		if len(vectors) != len(missing) {
			return result, domain.NewError(domain.ErrorCodeExternalUnavailable, "index.embed", "Embedding provider returned the wrong number of vectors.", "The provider must return one vector per input in input order.", nil)
		}
		records := make([]domain.ChunkEmbedding, len(missing))
		for i, c := range missing {
			if err := domain.ValidateVector(vectors[i], space.Dimensions); err != nil {
				return result, domain.NewError(domain.ErrorCodeExternalUnavailable, "index.embed", err.Error(), "Check the embedding provider's declared model and dimensions.", err)
			}
			records[i] = domain.ChunkEmbedding{Chunk: c, Vector: vectors[i]}
		}
		if err := s.saveBatch(ctx, repo, space, records); err != nil {
			return result, err
		}
		result.EmbeddedChunks += len(records)
	}
	return result, nil
}

// Search returns exact source passages and similarity scores, never verdicts or
// generated quotes. Stale cross-file results are omitted until the next prepare.
func (s *Service) Search(ctx context.Context, opts domain.SearchOptions) (domain.SearchResult, error) {
	result := domain.SearchResult{Repository: opts.Repository, Matches: []domain.SearchMatch{}}
	if err := s.validate(opts.Repository); err != nil {
		return result, err
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return result, invalid("search query must not be empty")
	}
	limit := opts.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 100 {
		return result, invalid("search limit must be between 1 and 100")
	}
	if err := s.requireVectors(); err != nil {
		return result, err
	}
	space, err := s.space()
	if err != nil {
		return result, err
	}
	result.Space = space
	if err := s.vectors.Check(ctx); err != nil {
		return result, err
	}
	// Do not pay to embed a query when this repository/model has no indexed
	// vectors yet (for example immediately after changing output dimensions).
	status, err := s.catalog.IndexStatus(ctx, opts.Repository)
	if err != nil {
		return result, err
	}
	ready := false
	for _, entry := range status.Spaces {
		if entry.Space.ID() == space.ID() && entry.EmbeddedChunks > 0 {
			ready = true
			break
		}
	}
	if !ready {
		return result, nil
	}
	vectors, err := s.embedder.Embed(ctx, domain.EmbeddingQuery, []string{query})
	if err != nil {
		return result, err
	}
	if len(vectors) != 1 {
		return result, domain.NewError(domain.ErrorCodeExternalUnavailable, "search", "Embedding provider must return exactly one query vector.", "Check the embedding adapter.", nil)
	}
	if err := domain.ValidateVector(vectors[0], space.Dimensions); err != nil {
		return result, domain.NewError(domain.ErrorCodeExternalUnavailable, "search", err.Error(), "Check the embedding adapter.", err)
	}
	matches, err := s.vectors.Search(ctx, opts.Repository, space, vectors[0], limit)
	if err != nil {
		return result, err
	}
	// With no score threshold, an indexed repository must have a remote candidate.
	// A restored-but-empty collection is data loss, not evidence of no similarities.
	if len(matches) == 0 {
		return result, domain.NewError(domain.ErrorCodeExternalUnavailable, "search", "Qdrant returned no vectors despite indexed catalog receipts.", "Run index reconcile, then restore or migrate existing vectors, or explicitly embed missing passages.", nil)
	}
	result.Matches, err = s.freshMatches(ctx, opts.Repository, matches)
	return result, err
}

func (s *Service) freshMatches(ctx context.Context, repo string, matches []domain.SearchMatch) ([]domain.SearchMatch, error) {
	result := []domain.SearchMatch{}
	details := make(map[int]domain.ItemDetail)
	missing := make(map[int]bool)
	for _, match := range matches {
		c := match.Chunk
		if c.Repository != repo {
			return nil, domain.NewError(domain.ErrorCodeExternalUnavailable, "search", "Vector storage returned a passage outside the requested repository.", "Inspect collection payloads and repository filters before retrying.", nil)
		}
		prepared, err := s.catalog.GetChunk(ctx, repo, c.ID)
		if err != nil {
			var domErr *domain.Error
			if errors.As(err, &domErr) && domErr.Code == domain.ErrorCodeNotFound {
				continue
			}
			return nil, err
		}
		if prepared.ContentHash != c.ContentHash || ChunkHash(c) != c.ContentHash {
			continue
		}
		if missing[c.ItemNumber] {
			continue
		}
		detail, loaded := details[c.ItemNumber]
		if !loaded {
			item, comments, err := s.sources.GetItemWithComments(ctx, repo, c.ItemNumber)
			if err != nil {
				var domErr *domain.Error
				if errors.As(err, &domErr) && domErr.Code == domain.ErrorCodeNotFound {
					missing[c.ItemNumber] = true
					continue
				}
				return nil, err
			}
			detail = domain.ItemDetail{Item: item, Comments: comments}
			details[c.ItemNumber] = detail
		}
		current, ok, err := currentChunk(c, detail)
		if err != nil {
			return nil, err
		}
		if ok {
			match.Chunk = current
			result = append(result, match)
		}
	}
	return result, nil
}

func currentChunk(c domain.SearchChunk, detail domain.ItemDetail) (domain.SearchChunk, bool, error) {
	if c.Repository != detail.Item.Repository || c.ItemNumber != detail.Item.Number || c.ItemKind != detail.Item.Kind {
		return c, false, nil
	}
	item := detail.Item
	var comments []domain.Comment
	switch c.SourceKind {
	case domain.SearchSourceTitle:
		item.Body = ""
	case domain.SearchSourceBody:
	case domain.SearchSourceComment:
		item.Body = ""
		for _, comment := range detail.Comments {
			if comment.CommentID == c.CommentID {
				comments = append(comments, comment)
				break
			}
		}
		if len(comments) == 0 {
			return c, false, nil
		}
	default:
		return c, false, nil
	}
	// Regenerate the selected source's windows. Checking only its old byte range
	// misses appends that change the final window or create a new overlapping one.
	chunks, err := Chunks(item, comments)
	if err != nil {
		return c, false, fmt.Errorf("validate current source passages: %w", err)
	}
	for _, chunk := range chunks {
		if chunk.ID == c.ID && chunk.ContentHash == c.ContentHash {
			return chunk, true, nil
		}
	}
	return c, false, nil
}

func (s *Service) validate(repo string) error {
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return invalid(err.Error())
	}
	if s.sources == nil || s.catalog == nil {
		return domain.NewError(domain.ErrorCodeNotConfigured, "retrieval", "Source storage and the passage catalog are required.", "Check database configuration with 'automain status'.", nil)
	}
	return nil
}

func (s *Service) requireVectors() error {
	if s.vectors == nil {
		return domain.NewError(domain.ErrorCodeNotConfigured, "retrieval", "Vector storage is not wired.", "Inspect 'automain status'; passage preparation does not require vector storage.", nil)
	}
	return nil
}

func (s *Service) space() (domain.EmbeddingSpace, error) {
	if s.embedder == nil {
		return domain.EmbeddingSpace{}, domain.NewError(domain.ErrorCodeNotConfigured, "retrieval", "Embedding provider is not wired.", "Check embedding configuration with 'automain status'. Source preparation does not require an embedding provider.", nil)
	}
	if validator, ok := s.embedder.(interface{ ValidateConfig() error }); ok {
		if err := validator.ValidateConfig(); err != nil {
			return domain.EmbeddingSpace{}, err
		}
	}
	return s.storedSpace()
}

// Stored-vector operations need the configured space identity, not provider keys.
func (s *Service) storedSpace() (domain.EmbeddingSpace, error) {
	if s.embedder == nil {
		return domain.EmbeddingSpace{}, domain.NewError(domain.ErrorCodeNotConfigured, "retrieval", "Embedding space is not configured.", "Inspect embedding settings with 'automain status'. No embedding request is needed for stored-vector operations.", nil)
	}
	space := s.embedder.Space()
	if err := space.Validate(); err != nil {
		return space, invalid(err.Error())
	}
	return space, nil
}

func invalid(message string) error {
	return domain.NewError(domain.ErrorCodeInvalidInput, "retrieval", message, "Check the repository, query, and indexing options.", nil)
}
