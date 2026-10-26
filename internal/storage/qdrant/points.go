package qdrant

import (
	"context"
	"math"
	"sort"

	"github.com/MRKups/automain/internal/domain"
	"github.com/google/uuid"
	q "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) SaveEmbeddings(ctx context.Context, repo string, space domain.EmbeddingSpace, embeddings []domain.ChunkEmbedding) error {
	const op = "qdrant.save_embeddings"
	if err := validateScope(op, repo, space); err != nil {
		return err
	}
	if len(embeddings) > maxBatch {
		return invalid(op, "Qdrant batch exceeds 256 embeddings.", nil)
	}
	chunks := make([]domain.SearchChunk, len(embeddings))
	total := 0
	for i, embedding := range embeddings {
		chunks[i] = embedding.Chunk
		if err := domain.ValidateVector(embedding.Vector, space.Dimensions); err != nil {
			return invalid(op, "Qdrant embedding must have the exact space dimension, finite values, and a nonzero norm.", err)
		}
		total += chunkBytes(embedding.Chunk) + 1024 + 4*len(embedding.Vector)
		if total > maxBatchBytes {
			return invalid(op, "Qdrant embedding batch exceeds 8 MiB.", nil)
		}
	}
	if err := validateChunks(op, repo, chunks); err != nil {
		return err
	}
	if len(embeddings) == 0 {
		return nil
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	// A drop between metadata validation and upsert is safe to retry once: IDs
	// are content-versioned and writes replace the same full payload and vector.
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := s.collection(ctx, op, space, true); err != nil {
			return err
		}
		points := make([]*q.PointStruct, len(embeddings))
		for i, embedding := range embeddings {
			points[i] = &q.PointStruct{Id: q.NewID(domain.PointID(embedding.Chunk)), Vectors: q.NewVectorsDense(embedding.Vector), Payload: payload(embedding.Chunk, space)}
		}
		reply, err := s.client.Points().Upsert(ctx, &q.UpsertPoints{CollectionName: s.prefix + space.ID(), Wait: q.PtrOf(true), Points: points})
		if status.Code(err) == codes.NotFound && attempt == 0 {
			continue
		}
		if err != nil {
			return unavailable(op, err)
		}
		if !completed(reply.GetResult()) {
			return malformed(op)
		}
		return nil
	}
	return malformed(op)
}

func (s *Store) Existing(ctx context.Context, repo string, space domain.EmbeddingSpace, chunks []domain.SearchChunk) (map[string]bool, error) {
	const op = "qdrant.existing"
	if err := validateScope(op, repo, space); err != nil {
		return nil, err
	}
	if err := validateChunks(op, repo, chunks); err != nil {
		return nil, err
	}
	found := make(map[string]bool, len(chunks))
	if len(chunks) == 0 {
		return found, nil
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	info, err := s.collection(ctx, op, space, false)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return found, nil
	}
	ids := make([]*q.PointId, len(chunks))
	expected := make(map[string]domain.SearchChunk, len(chunks))
	for i, chunk := range chunks {
		ids[i] = q.NewID(domain.PointID(chunk))
		expected[domain.PointID(chunk)] = chunk
	}
	reply, err := s.client.Points().Get(ctx, &q.GetPoints{CollectionName: s.prefix + space.ID(), Ids: ids, WithPayload: q.NewWithPayload(true), WithVectors: q.NewWithVectors(false)})
	if status.Code(err) == codes.NotFound {
		return found, nil
	}
	if err != nil {
		return nil, unavailable(op, err)
	}
	if reply == nil || len(reply.GetResult()) > len(chunks) {
		return nil, malformed(op)
	}
	seen := make(map[string]bool)
	for _, point := range reply.GetResult() {
		want, ok := expected[point.GetId().GetUuid()]
		if !ok || seen[point.GetId().GetUuid()] {
			return nil, malformed(op)
		}
		seen[point.GetId().GetUuid()] = true
		chunk, err := decodePayload(op, repo, space, point.GetId(), point.GetPayload())
		if err != nil {
			return nil, err
		}
		if sameEmbedding(chunk, want) {
			found[chunk.ID] = true
		}
	}
	return found, nil
}

func (s *Store) DeletePoints(ctx context.Context, repo string, space domain.EmbeddingSpace, pointIDs []string) error {
	const op = "qdrant.delete_points"
	if err := validateScope(op, repo, space); err != nil {
		return err
	}
	if len(pointIDs) > maxBatch {
		return invalid(op, "Qdrant deletion batch exceeds 256 points.", nil)
	}
	ids := make([]*q.PointId, len(pointIDs))
	for i, id := range pointIDs {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			return invalid(op, "Qdrant deletion requires canonical UUID point IDs.", err)
		}
		ids[i] = q.NewID(id)
	}
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	info, err := s.collection(ctx, op, space, false)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	filter := scopeFilter(repo, space)
	filter.Must = append(filter.Must, q.NewHasID(ids...))
	reply, err := s.client.Points().Delete(ctx, &q.DeletePoints{CollectionName: s.prefix + space.ID(), Wait: q.PtrOf(true), Points: q.NewPointsSelectorFilter(filter)})
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return unavailable(op, err)
	}
	if !completed(reply.GetResult()) {
		return malformed(op)
	}
	return nil
}

func (s *Store) Search(ctx context.Context, repo string, space domain.EmbeddingSpace, vector []float32, limit int) ([]domain.SearchMatch, error) {
	const op = "qdrant.search"
	if err := validateSearch(op, repo, space, limit); err != nil {
		return nil, err
	}
	if err := domain.ValidateVector(vector, space.Dimensions); err != nil {
		return nil, invalid(op, "Qdrant query vector must have the exact space dimension, finite values, and a nonzero norm.", err)
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	info, err := s.collection(ctx, op, space, false)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, missingCollection(op, nil)
	}
	return s.query(ctx, op, repo, space, q.NewQueryDense(vector), limit, "")
}

func (s *Store) SearchByChunk(ctx context.Context, repo string, space domain.EmbeddingSpace, chunk domain.SearchChunk, limit int) ([]domain.SearchMatch, error) {
	const op = "qdrant.search_by_chunk"
	if err := validateSearch(op, repo, space, limit); err != nil {
		return nil, err
	}
	if err := validateChunk(op, repo, chunk); err != nil {
		return nil, err
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	info, err := s.collection(ctx, op, space, false)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, missing(op)
	}
	id := q.NewID(domain.PointID(chunk))
	reply, err := s.client.Points().Get(ctx, &q.GetPoints{CollectionName: s.prefix + space.ID(), Ids: []*q.PointId{id}, WithPayload: q.NewWithPayload(true), WithVectors: q.NewWithVectors(false)})
	if status.Code(err) == codes.NotFound {
		return nil, missing(op)
	}
	if err != nil {
		return nil, unavailable(op, err)
	}
	if reply == nil {
		return nil, malformed(op)
	}
	if len(reply.GetResult()) == 0 {
		return nil, missing(op)
	}
	if len(reply.GetResult()) != 1 {
		return nil, malformed(op)
	}
	point := reply.GetResult()[0]
	stored, err := decodePayload(op, repo, space, point.GetId(), point.GetPayload())
	if err != nil {
		return nil, err
	}
	if !sameEmbedding(stored, chunk) {
		return nil, missing(op)
	}
	return s.query(ctx, op, repo, space, q.NewQueryID(id), limit, itemKey(chunk))
}

func validateSearch(op, repo string, space domain.EmbeddingSpace, limit int) error {
	if err := validateScope(op, repo, space); err != nil {
		return err
	}
	if limit < 1 || limit > 100 {
		return invalid(op, "Qdrant search limit must be between 1 and 100.", nil)
	}
	return nil
}

func scopeFilter(repo string, space domain.EmbeddingSpace) *q.Filter {
	return &q.Filter{Must: []*q.Condition{q.NewMatchKeyword("repository", repo), q.NewMatchKeyword("space_id", space.ID())}}
}

func (s *Store) query(ctx context.Context, op, repo string, space domain.EmbeddingSpace, query *q.Query, limit int, excludeItem string) ([]domain.SearchMatch, error) {
	filter := scopeFilter(repo, space)
	if excludeItem != "" {
		filter.MustNot = []*q.Condition{q.NewMatchKeyword("item_key", excludeItem)}
	}
	reply, err := s.client.Points().Query(ctx, &q.QueryPoints{
		CollectionName: s.prefix + space.ID(), Query: query, Filter: filter,
		Params: &q.SearchParams{Exact: q.PtrOf(true)}, Limit: q.PtrOf(uint64(limit)),
		WithPayload: q.NewWithPayload(true), WithVectors: q.NewWithVectors(false),
	})
	if status.Code(err) == codes.NotFound {
		if excludeItem != "" {
			return nil, missing(op)
		}
		return nil, missingCollection(op, err)
	}
	if err != nil {
		return nil, unavailable(op, err)
	}
	if reply == nil || len(reply.GetResult()) > limit {
		return nil, malformed(op)
	}
	matches := make([]domain.SearchMatch, 0, len(reply.GetResult()))
	seen := make(map[string]bool)
	for _, point := range reply.GetResult() {
		chunk, err := decodePayload(op, repo, space, point.GetId(), point.GetPayload())
		if err != nil {
			return nil, err
		}
		score := float64(point.GetScore())
		if math.IsNaN(score) || math.IsInf(score, 0) || seen[point.GetId().GetUuid()] || (excludeItem != "" && itemKey(chunk) == excludeItem) {
			return nil, malformed(op)
		}
		seen[point.GetId().GetUuid()] = true
		matches = append(matches, domain.SearchMatch{Chunk: chunk, Score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].Chunk.ID < matches[j].Chunk.ID
		}
		return matches[i].Score > matches[j].Score
	})
	return matches, nil
}
