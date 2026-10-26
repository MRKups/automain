package qdrant

import (
	"fmt"
	"unicode/utf8"

	"github.com/MRKups/automain/internal/domain"
	q "github.com/qdrant/go-client/qdrant"
)

func itemKey(c domain.SearchChunk) string {
	return fmt.Sprintf("%s/%s/%d", c.Repository, c.ItemKind, c.ItemNumber)
}

// Author and URL are refreshed from canonical sources on reads. They are not
// embedding inputs, so a metadata-only update must not trigger paid re-embedding.
func sameEmbedding(stored, current domain.SearchChunk) bool {
	stored.Author, current.Author = "", ""
	stored.URL, current.URL = "", ""
	return stored == current
}

func chunkBytes(c domain.SearchChunk) int {
	return len(c.ID) + len(c.Repository) + len(c.ItemKind) + len(c.SourceKind) + len(c.CommentID) + len(c.Title) + len(c.Text) + len(c.Author) + len(c.URL) + len(c.ContentHash)
}

func validateChunk(op, repo string, c domain.SearchChunk) error {
	if err := c.Validate(); err != nil {
		return invalid(op, "Qdrant passage is invalid.", err)
	}
	if c.Repository != repo {
		return invalid(op, "Qdrant passage repository does not match the operation scope.", nil)
	}
	if chunkBytes(c) > maxChunkBytes {
		return invalid(op, "Qdrant passage payload exceeds 64 KiB.", nil)
	}
	for _, value := range []string{c.ID, c.Repository, string(c.ItemKind), c.SourceKind, c.CommentID, c.Title, c.Text, c.Author, c.URL, c.ContentHash} {
		if !utf8.ValidString(value) {
			return invalid(op, "Qdrant passage contains invalid UTF-8.", nil)
		}
	}
	return nil
}

func payload(c domain.SearchChunk, space domain.EmbeddingSpace) map[string]*q.Value {
	return map[string]*q.Value{
		"id": q.NewValueString(c.ID), "repository": q.NewValueString(c.Repository),
		"item_number": q.NewValueInt(int64(c.ItemNumber)), "item_kind": q.NewValueString(string(c.ItemKind)),
		"item_key": q.NewValueString(itemKey(c)), "space_id": q.NewValueString(space.ID()),
		"source_kind": q.NewValueString(c.SourceKind), "comment_id": q.NewValueString(c.CommentID),
		"start_byte": q.NewValueInt(int64(c.StartByte)), "end_byte": q.NewValueInt(int64(c.EndByte)),
		"title": q.NewValueString(c.Title), "text": q.NewValueString(c.Text),
		"author": q.NewValueString(c.Author), "url": q.NewValueString(c.URL), "content_hash": q.NewValueString(c.ContentHash),
	}
}

func decodePayload(op, repo string, space domain.EmbeddingSpace, id *q.PointId, values map[string]*q.Value) (domain.SearchChunk, error) {
	var c domain.SearchChunk
	strings := map[string]*string{
		"id": &c.ID, "repository": &c.Repository, "source_kind": &c.SourceKind,
		"comment_id": &c.CommentID, "title": &c.Title, "text": &c.Text,
		"author": &c.Author, "url": &c.URL, "content_hash": &c.ContentHash,
	}
	var kind, spaceID, key string
	strings["item_kind"], strings["space_id"], strings["item_key"] = &kind, &spaceID, &key
	for field, dest := range strings {
		value, ok := values[field].GetKind().(*q.Value_StringValue)
		if !ok {
			return c, malformed(op)
		}
		*dest = value.StringValue
	}
	c.ItemKind = domain.ItemKind(kind)
	for field, dest := range map[string]*int{"item_number": &c.ItemNumber, "start_byte": &c.StartByte, "end_byte": &c.EndByte} {
		value, ok := values[field].GetKind().(*q.Value_IntegerValue)
		if !ok || int64(int(value.IntegerValue)) != value.IntegerValue {
			return c, malformed(op)
		}
		*dest = int(value.IntegerValue)
	}
	if validateChunk(op, repo, c) != nil || spaceID != space.ID() || key != itemKey(c) || id.GetUuid() != domain.PointID(c) {
		return c, malformed(op)
	}
	return c, nil
}

func validateChunks(op, repo string, chunks []domain.SearchChunk) error {
	if len(chunks) > maxBatch {
		return invalid(op, "Qdrant batch exceeds 256 passages.", nil)
	}
	seen := make(map[string]bool, len(chunks))
	total := 0
	for _, c := range chunks {
		if err := validateChunk(op, repo, c); err != nil {
			return err
		}
		if seen[c.ID] {
			return invalid(op, "Qdrant batch contains a duplicate passage ID.", nil)
		}
		seen[c.ID] = true
		total += chunkBytes(c) + 1024
		if total > maxBatchBytes {
			return invalid(op, "Qdrant batch exceeds 8 MiB.", nil)
		}
	}
	return nil
}
