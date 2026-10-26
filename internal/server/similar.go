package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/MRKups/automain/internal/domain"
)

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	const command = "similar"
	if !requireMethod(w, r, command, http.MethodGet, http.MethodPost) {
		return
	}
	if err := validateRetrievalQuery(r, command, "repo", "repository", "chunk_id", "item_number", "limit"); err != nil {
		writeAPIError(w, command, err)
		return
	}
	var body struct {
		Repository string  `json:"repository"`
		ChunkID    *string `json:"chunk_id"`
		ItemNumber *int    `json:"item_number"`
		Limit      *int    `json:"limit"`
	}
	if r.Method == http.MethodPost {
		if err := decodeRetrievalJSONBody(r, &body, "repository", "chunk_id", "item_number", "limit"); err != nil {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				fmt.Sprintf("Invalid JSON request body: %v", err), "Pass repository, exactly one of chunk_id or item_number, and optional limit.", err))
			return
		}
	}
	q := r.URL.Query()
	if q.Has("chunk_id") {
		value := q.Get("chunk_id")
		body.ChunkID = &value
	}
	if q.Has("item_number") {
		value, err := strconv.Atoi(q.Get("item_number"))
		if err != nil {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				"Similarity item number must be an integer.", "Pass a positive item_number or use chunk_id instead.", err))
			return
		}
		body.ItemNumber = &value
	}
	limit := 10
	if body.Limit != nil {
		limit = *body.Limit
	}
	if q.Has("limit") {
		value, err := strconv.Atoi(q.Get("limit"))
		if err != nil {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				"Similarity limit must be an integer.", "Pass limit between 1 and 100.", err))
			return
		}
		limit = value
	}
	if (body.ChunkID == nil) == (body.ItemNumber == nil) || (body.ChunkID != nil && strings.TrimSpace(*body.ChunkID) == "") || (body.ItemNumber != nil && *body.ItemNumber <= 0) || limit < 1 || limit > 100 {
		writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
			"Similarity requires exactly one non-empty chunk_id or positive item_number, and a limit between 1 and 100.", "Pass repository and exactly one source selector; omit limit to use 10.", nil))
		return
	}
	opts := domain.SimilarOptions{Repository: firstNonEmpty(queryRepo(r), body.Repository), Limit: limit}
	if body.ChunkID != nil {
		opts.ChunkID = strings.TrimSpace(*body.ChunkID)
	}
	if body.ItemNumber != nil {
		opts.ItemNumber = *body.ItemNumber
	}
	result, err := s.svc.Similar(ctx, opts)
	if err != nil {
		writeAPIError(w, command, err)
		return
	}
	writeAPISuccess(w, http.StatusOK, command, result)
}

func (s *Server) handleMigrate(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	const command = "index.migrate"
	if !requireMethod(w, r, command, http.MethodPost) {
		return
	}
	if err := validateRetrievalQuery(r, command, "repo", "repository", "limit"); err != nil {
		writeAPIError(w, command, err)
		return
	}
	var body struct {
		Repository string `json:"repository"`
		Path       string `json:"path"`
		Limit      *int   `json:"limit"`
	}
	if err := decodeRetrievalJSONBody(r, &body, "repository", "path", "limit"); err != nil {
		writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
			fmt.Sprintf("Invalid JSON request body: %v", err), "Pass repository, an explicit legacy source path, and optional limit.", err))
		return
	}
	limit := 100
	if body.Limit != nil {
		limit = *body.Limit
	}
	if raw := r.URL.Query(); raw.Has("limit") {
		value, err := strconv.Atoi(raw.Get("limit"))
		if err != nil {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				"Migration limit must be an integer.", "Pass limit between 1 and 10000.", err))
			return
		}
		limit = value
	}
	path := strings.TrimSpace(body.Path)
	if path == "" || limit < 1 || limit > 10000 {
		writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
			"Migration requires an explicit legacy path and a limit between 1 and 10000.", "Pass path in the JSON body; omit limit to use 100.", nil))
		return
	}
	result, err := s.svc.MigrateIndex(ctx, firstNonEmpty(queryRepo(r), body.Repository), path, limit)
	if err != nil {
		writeAPIError(w, command, err)
		return
	}
	writeAPISuccess(w, http.StatusOK, command, result)
}
