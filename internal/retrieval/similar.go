package retrieval

import (
	"context"
	"sort"

	"github.com/MRKups/automain/internal/domain"
)

const maxQueryPassages = 32

// Similar reuses current stored passage vectors and groups candidates by item.
// The bounded source selection is reported explicitly, never silently embedded.
func (s *Service) Similar(ctx context.Context, opts domain.SimilarOptions) (domain.SimilarResult, error) {
	result := domain.SimilarResult{Repository: opts.Repository, Matches: []domain.ItemSearchMatch{}}
	if err := s.validate(opts.Repository); err != nil {
		return result, err
	}
	if (opts.ChunkID == "") == (opts.ItemNumber == 0) || opts.ItemNumber < 0 {
		return result, invalid("similar requires either one chunk ID or one positive item number")
	}
	limit := opts.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 100 {
		return result, invalid("similar limit must be between 1 and 100 items")
	}
	if err := s.requireVectors(); err != nil {
		return result, err
	}
	space, err := s.storedSpace()
	if err != nil {
		return result, err
	}
	result.Space = space
	if err := s.vectors.Check(ctx); err != nil {
		return result, err
	}
	queries := []domain.SearchChunk{}
	hadPrepared := false
	if opts.ChunkID != "" {
		chunk, err := s.catalog.GetChunk(ctx, opts.Repository, opts.ChunkID)
		if err != nil {
			return result, err
		}
		queries = append(queries, chunk)
		hadPrepared = true
	} else {
		after := ""
		for len(queries) <= maxQueryPassages {
			chunks, err := s.catalog.ListChunks(ctx, opts.Repository, after, 256)
			if err != nil {
				return result, err
			}
			if len(chunks) == 0 {
				break
			}
			candidates := []domain.SearchChunk{}
			for _, chunk := range chunks {
				if chunk.ItemNumber == opts.ItemNumber {
					hadPrepared = true
					candidates = append(candidates, chunk)
				}
			}
			available, err := s.vectors.Existing(ctx, opts.Repository, space, candidates)
			if err != nil {
				return result, err
			}
			for _, chunk := range candidates {
				if available[chunk.ID] {
					queries = append(queries, chunk)
					if len(queries) > maxQueryPassages {
						break
					}
				}
			}
			after = chunks[len(chunks)-1].ID
		}
	}
	if len(queries) == 0 {
		if hadPrepared {
			return result, domain.NewError(domain.ErrorCodeNotFound, "similar", "The selected item's prepared passages have no current stored vectors in this embedding space.", "Run index reconcile and index embed before requesting similarity.", nil)
		}
		return result, domain.NewError(domain.ErrorCodeNotFound, "similar", "No prepared passages were found for the selected source.", "Run index preparation and embedding for this repository, then choose an indexed source.", nil)
	}
	if len(queries) > maxQueryPassages {
		queries = queries[:maxQueryPassages]
		result.Truncated = true
	}
	present, err := s.vectors.Existing(ctx, opts.Repository, space, queries)
	if err != nil {
		return result, err
	}
	all := []domain.SearchMatch{}
	used := []domain.SearchChunk{}
	for _, query := range queries {
		if !present[query.ID] {
			if opts.ChunkID != "" {
				return result, domain.NewError(domain.ErrorCodeNotFound, "similar", "The selected passage has no current stored vector in this embedding space.", "Run index reconcile and index embed before requesting similarity.", nil)
			}
			continue
		}
		if err := s.ensureQueryCurrent(ctx, query); err != nil {
			return result, err
		}
		matches, err := s.vectors.SearchByChunk(ctx, opts.Repository, space, query, min(100, limit*4))
		if err != nil {
			return result, err
		}
		all = append(all, matches...)
		used = append(used, query)
	}
	result.QueryPassages = len(used)
	if len(used) == 0 {
		return result, domain.NewError(domain.ErrorCodeNotFound, "similar", "The selected source passages no longer have current stored vectors in this embedding space.", "Run index reconcile and retry, or index missing passages explicitly.", nil)
	}
	for _, query := range used {
		if err := s.ensureQueryCurrent(ctx, query); err != nil {
			return result, err
		}
	}
	fresh, err := s.freshMatches(ctx, opts.Repository, all)
	if err != nil {
		return result, err
	}
	sort.Slice(fresh, func(i, j int) bool {
		if fresh[i].Score != fresh[j].Score {
			return fresh[i].Score > fresh[j].Score
		}
		return fresh[i].Chunk.ID < fresh[j].Chunk.ID
	})
	groups := map[int]int{}
	seen := map[string]bool{}
	for _, match := range fresh {
		if match.Chunk.ItemNumber == used[0].ItemNumber || seen[match.Chunk.ID] {
			continue
		}
		seen[match.Chunk.ID] = true
		index, exists := groups[match.Chunk.ItemNumber]
		if !exists {
			if len(result.Matches) == limit {
				continue
			}
			index = len(result.Matches)
			groups[match.Chunk.ItemNumber] = index
			result.Matches = append(result.Matches, domain.ItemSearchMatch{
				ItemNumber: match.Chunk.ItemNumber, ItemKind: match.Chunk.ItemKind,
				Score: match.Score, Passages: []domain.SearchMatch{},
			})
		}
		if len(result.Matches[index].Passages) < 3 {
			result.Matches[index].Passages = append(result.Matches[index].Passages, match)
		}
	}
	return result, nil
}

func (s *Service) ensureQueryCurrent(ctx context.Context, query domain.SearchChunk) error {
	item, comments, err := s.sources.GetItemWithComments(ctx, query.Repository, query.ItemNumber)
	if err != nil {
		return err
	}
	_, current, err := currentChunk(query, domain.ItemDetail{Item: item, Comments: comments})
	if err != nil {
		return err
	}
	if !current {
		return domain.NewError(domain.ErrorCodeInvalidInput, "similar", "The source passage changed after it was prepared.", "Run index embed to refresh this source before requesting similarity.", nil)
	}
	return nil
}
