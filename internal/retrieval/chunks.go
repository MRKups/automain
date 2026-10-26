package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MRKups/automain/internal/domain"
)

// These are character limits, not model-specific token counts. Provider adapters
// remain responsible for enforcing their input limits rather than truncating.
const (
	chunkRunes   = 1200
	overlapRunes = 150
	chunkRecipe  = "source-runes-v1"
)

// Chunks preserves exact source passages, UTF-8 boundaries, and byte offsets.
// A title is indexed separately so an item with an empty body is still searchable.
func Chunks(item domain.Item, comments []domain.Comment) ([]domain.SearchChunk, error) {
	if err := domain.ValidateRepoSlug(item.Repository); err != nil {
		return nil, err
	}
	if item.Number <= 0 {
		return nil, fmt.Errorf("search source item number must be positive")
	}
	if err := item.Kind.Validate(); err != nil {
		return nil, err
	}
	chunks := []domain.SearchChunk{}
	appendText := func(kind, commentID, text, author, url string) error {
		if !utf8.ValidString(text) || !utf8.ValidString(item.Title) {
			return fmt.Errorf("search source must contain valid UTF-8")
		}
		bounds := make([]int, 0, utf8.RuneCountInString(text)+1)
		for pos := range text {
			bounds = append(bounds, pos)
		}
		bounds = append(bounds, len(text))
		for start, ordinal := 0, 0; start < len(bounds)-1; ordinal++ {
			end := min(start+chunkRunes, len(bounds)-1)
			passage := text[bounds[start]:bounds[end]]
			if strings.TrimSpace(passage) != "" {
				c := domain.SearchChunk{
					Repository: item.Repository, ItemNumber: item.Number, ItemKind: item.Kind,
					SourceKind: kind, CommentID: commentID, StartByte: bounds[start], EndByte: bounds[end],
					Title: item.Title, Text: passage, Author: author, URL: url,
				}
				c.ID = digest(item.Repository, fmt.Sprint(item.Number), kind, commentID, fmt.Sprint(ordinal), chunkRecipe)
				c.ContentHash = ChunkHash(c)
				chunks = append(chunks, c)
			}
			if end == len(bounds)-1 {
				break
			}
			start = end - overlapRunes
		}
		return nil
	}
	if err := appendText(domain.SearchSourceTitle, "", item.Title, item.Author, item.URL); err != nil {
		return nil, err
	}
	if err := appendText(domain.SearchSourceBody, "", item.Body, item.Author, item.URL); err != nil {
		return nil, err
	}
	seenComments := make(map[string]bool, len(comments))
	for _, comment := range comments {
		if comment.Repository != item.Repository || comment.ItemNumber != item.Number || strings.TrimSpace(comment.CommentID) == "" || seenComments[comment.CommentID] {
			return nil, fmt.Errorf("invalid or duplicate comment source for %s#%d", item.Repository, item.Number)
		}
		seenComments[comment.CommentID] = true
		if err := appendText(domain.SearchSourceComment, comment.CommentID, comment.Body, comment.Author, comment.URL); err != nil {
			return nil, err
		}
	}
	return chunks, nil
}

// ChunkHash covers the embedding input and source identity, not mutable metadata.
func ChunkHash(c domain.SearchChunk) string {
	return digest(chunkRecipe, c.Repository, fmt.Sprint(c.ItemNumber), string(c.ItemKind), c.SourceKind, c.CommentID,
		fmt.Sprint(c.StartByte), fmt.Sprint(c.EndByte), c.InputText())
}

func digest(values ...string) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
