package retrieval

import (
	"context"
	"errors"

	"github.com/MRKups/automain/internal/domain"
)

// Migrate copies compatible stored vectors; it never invokes an embedding provider.
// Each run scans past completed entries so bounded repeat runs make progress.
func (s *Service) Migrate(ctx context.Context, repo string, legacy domain.LegacyIndex, limit int) (domain.MigrationResult, error) {
	result := domain.MigrationResult{Repository: repo}
	if legacy == nil {
		return result, invalid("a read-only legacy vector source is required")
	}
	if limit < 1 || limit > 10000 {
		return result, invalid("migration limit must be between 1 and 10000 vector writes")
	}
	prepared, err := s.Prepare(ctx, repo)
	if err != nil {
		return result, err
	}
	if err := s.requireVectors(); err != nil {
		return result, err
	}
	if err := s.reconcile(ctx, repo, &prepared); err != nil {
		return result, err
	}
	spaces, err := legacy.Spaces(ctx)
	if err != nil {
		return result, err
	}
	result.Spaces = len(spaces)
	for _, space := range spaces {
		if err := space.Validate(); err != nil {
			return result, invalid("legacy embedding space metadata is invalid; preserve the source file and inspect its metadata")
		}
		if err := s.catalog.RegisterSpace(ctx, space); err != nil {
			return result, err
		}
		after := ""
		for result.MigratedChunks < limit {
			stored, err := legacy.ReadEmbeddings(ctx, repo, space, after, min(32, limit-result.MigratedChunks))
			if err != nil {
				return result, err
			}
			if len(stored) == 0 {
				break
			}
			after = stored[len(stored)-1].Chunk.ID
			current := []domain.SearchChunk{}
			records := []domain.ChunkEmbedding{}
			for _, record := range stored {
				chunk, err := s.catalog.GetChunk(ctx, repo, record.Chunk.ID)
				if err != nil {
					var domErr *domain.Error
					if !errors.As(err, &domErr) || domErr.Code != domain.ErrorCodeNotFound {
						return result, err
					}
					result.SkippedChunks++
					continue
				}
				if record.Chunk.Repository != repo || record.Chunk.ContentHash != chunk.ContentHash || ChunkHash(record.Chunk) != chunk.ContentHash {
					result.SkippedChunks++
					continue
				}
				if err := domain.ValidateVector(record.Vector, space.Dimensions); err != nil {
					return result, invalid("legacy vector is malformed; preserve the source file and inspect the reported embedding space")
				}
				record.Chunk = chunk
				records = append(records, record)
				current = append(current, chunk)
			}
			if len(current) == 0 {
				continue
			}
			present, err := s.vectors.Existing(ctx, repo, space, current)
			if err != nil {
				return result, err
			}
			missing := []domain.ChunkEmbedding{}
			reused := []domain.SearchChunk{}
			for _, record := range records {
				if present[record.Chunk.ID] {
					reused = append(reused, record.Chunk)
				} else {
					missing = append(missing, record)
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
			if len(missing) > 0 {
				if err := s.saveBatch(ctx, repo, space, missing); err != nil {
					return result, err
				}
				result.MigratedChunks += len(missing)
			}
		}
		if result.MigratedChunks == limit {
			break
		}
	}
	return result, nil
}
