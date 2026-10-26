package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/MRKups/automain/internal/domain"
)

func (s *Server) handleRetrieval(w http.ResponseWriter, r *http.Request, ctx context.Context, subPath string) {
	if subPath == "/index/status" {
		if !requireMethod(w, r, "index.status", http.MethodGet, http.MethodHead) {
			return
		}
		if err := validateRetrievalQuery(r, "index.status", "repo", "repository"); err != nil {
			writeAPIError(w, "index.status", err)
			return
		}
		result, err := s.svc.IndexStatus(ctx, queryRepo(r))
		if err != nil {
			writeAPIError(w, "index.status", err)
			return
		}
		writeAPISuccess(w, http.StatusOK, "index.status", result)
		return
	}
	command := "index.prepare"
	if subPath == "/index/embed" {
		command = "index.embed"
	} else if subPath == "/index/reconcile" {
		command = "index.reconcile"
	} else if subPath == "/search" {
		command = "search"
	}
	if command == "search" {
		if !requireMethod(w, r, command, http.MethodGet, http.MethodPost) {
			return
		}
	} else if !requireMethod(w, r, command, http.MethodPost) {
		return
	}
	var body struct {
		Repository string `json:"repository"`
		Query      string `json:"query"`
		Limit      *int   `json:"limit"`
	}
	queryFields := []string{"repo", "repository"}
	bodyFields := []string{"repository"}
	if command == "index.embed" || command == "search" {
		queryFields = append(queryFields, "limit")
		bodyFields = append(bodyFields, "limit")
	}
	if command == "search" {
		queryFields = append(queryFields, "q", "query")
		bodyFields = append(bodyFields, "query")
	}
	if err := validateRetrievalQuery(r, command, queryFields...); err != nil {
		writeAPIError(w, command, err)
		return
	}
	if r.Method == http.MethodPost {
		if err := decodeRetrievalJSONBody(r, &body, bodyFields...); err != nil {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				fmt.Sprintf("Invalid JSON request body: %v", err), "Pass repository, query (search only), and limit (embed/search only).", err))
			return
		}
	}
	repo := firstNonEmpty(queryRepo(r), body.Repository)
	limit := 100
	if command == "search" {
		limit = 10
	}
	if body.Limit != nil {
		limit = *body.Limit
	}
	if raw := r.URL.Query(); raw.Has("limit") {
		parsed, err := strconv.Atoi(raw.Get("limit"))
		if err != nil {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command, "Limit must be an integer.", "Use a positive chunk/result limit.", err))
			return
		}
		limit = parsed
	}
	if command == "search" {
		if limit < 1 || limit > 100 {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command, "Search limit must be between 1 and 100.", "Use limit=10, for example.", nil))
			return
		}
		result, err := s.svc.Search(ctx, domain.SearchOptions{Repository: repo, Query: firstNonEmpty(r.URL.Query().Get("q"), r.URL.Query().Get("query"), body.Query), Limit: limit})
		if err != nil {
			writeAPIError(w, command, err)
			return
		}
		writeAPISuccess(w, http.StatusOK, command, result)
		return
	}
	var result domain.IndexResult
	var err error
	if command == "index.embed" {
		if limit < 1 || limit > 10000 {
			writeAPIError(w, command, domain.NewError(domain.ErrorCodeInvalidInput, command,
				"Embedding limit must be between 1 and 10000.", "Use limit=100, for example.", nil))
			return
		}
		result, err = s.svc.EmbedIndex(ctx, repo, limit)
	} else {
		if command == "index.reconcile" {
			result, err = s.svc.ReconcileIndex(ctx, repo)
		} else {
			result, err = s.svc.PrepareIndex(ctx, repo)
		}
	}
	if err != nil {
		writeAPIError(w, command, err)
		return
	}
	writeAPISuccess(w, http.StatusOK, command, result)
}
