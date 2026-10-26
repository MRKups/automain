package retrieval

import (
	"context"
	"errors"

	"github.com/MRKups/automain/internal/domain"
)

// Reconcile repairs remote presence and resumes durable deletions without models.
func (s *Service) Reconcile(ctx context.Context, repo string) (domain.IndexResult, error) {
	result, err := s.Prepare(ctx, repo)
	if err != nil {
		return result, err
	}
	if err := s.requireVectors(); err != nil {
		return result, err
	}
	err = s.reconcile(ctx, repo, &result)
	return result, err
}

func (s *Service) reconcile(ctx context.Context, repo string, result *domain.IndexResult) error {
	if err := s.vectors.Check(ctx); err != nil {
		return err
	}
	for {
		pending, err := s.catalog.PendingDeletions(ctx, repo, 128)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			break
		}
		// Delete operations are idempotent. Keep receipts until both stores agree.
		groups := map[string][]domain.VectorDeletion{}
		order := []string{}
		for _, deletion := range pending {
			key := deletion.Space.ID()
			if len(groups[key]) == 0 {
				order = append(order, key)
			}
			groups[key] = append(groups[key], deletion)
		}
		for _, key := range order {
			deletions := groups[key]
			ids := make([]string, len(deletions))
			for i, deletion := range deletions {
				ids[i] = deletion.PointID
			}
			if err := s.vectors.DeletePoints(ctx, repo, deletions[0].Space, ids); err != nil {
				return err
			}
			if err := s.catalog.CompleteDeletions(ctx, deletions); err != nil {
				return err
			}
			result.DeletedPoints += len(deletions)
		}
	}
	status, err := s.catalog.IndexStatus(ctx, repo)
	if err != nil {
		return err
	}
	for _, entry := range status.Spaces {
		after := ""
		for {
			chunks, err := s.catalog.IndexedChunks(ctx, repo, entry.Space, after, 128)
			if err != nil {
				return err
			}
			if len(chunks) == 0 {
				break
			}
			present, err := s.vectors.Existing(ctx, repo, entry.Space, chunks)
			if err != nil {
				return err
			}
			missing := []domain.SearchChunk{}
			for _, chunk := range chunks {
				if !present[chunk.ID] {
					missing = append(missing, chunk)
				}
			}
			if len(missing) > 0 {
				if err := s.catalog.MarkMissing(ctx, repo, entry.Space, missing); err != nil {
					return err
				}
			}
			after = chunks[len(chunks)-1].ID
		}
	}
	return nil
}

func (s *Service) saveBatch(ctx context.Context, repo string, space domain.EmbeddingSpace, records []domain.ChunkEmbedding) error {
	chunks := make([]domain.SearchChunk, len(records))
	for i, record := range records {
		chunks[i] = record.Chunk
	}
	if err := s.catalog.TrackChunks(ctx, repo, space, chunks); err != nil {
		return err
	}
	if err := s.vectors.SaveEmbeddings(ctx, repo, space, records); err != nil {
		return err
	}
	return s.confirmCurrent(ctx, repo, space, chunks)
}

func (s *Service) confirmCurrent(ctx context.Context, repo string, space domain.EmbeddingSpace, chunks []domain.SearchChunk) error {
	for _, chunk := range chunks {
		item, comments, err := s.sources.GetItemWithComments(ctx, repo, chunk.ItemNumber)
		if err != nil {
			var domErr *domain.Error
			if !errors.As(err, &domErr) || domErr.Code != domain.ErrorCodeNotFound {
				return err
			}
		} else {
			_, current, checkErr := currentChunk(chunk, domain.ItemDetail{Item: item, Comments: comments})
			if checkErr != nil {
				return checkErr
			}
			if current {
				continue
			}
		}
		// TrackChunks already preserved the old point identity. Preparation now
		// tombstones it, even if the remote upsert finished during a source edit.
		if _, prepErr := s.Prepare(ctx, repo); prepErr != nil {
			return prepErr
		}
		return domain.NewError(domain.ErrorCodeInvalidInput, "index.embed", "Source content changed during indexing; the old remote point remains scheduled for cleanup.", "Run 'index reconcile' and retry the bounded indexing operation.", nil)
	}
	return s.catalog.ConfirmChunks(ctx, repo, space, chunks)
}
