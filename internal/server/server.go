package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/app"
	"github.com/MRKups/automain/internal/domain"
	"github.com/MRKups/automain/web"
)

const (
	maxRequestBodyBytes = 1 << 20
	shutdownGracePeriod = 5 * time.Second
)

// Envelope is the JSON response envelope returned by all HTTP API endpoints.
type Envelope struct {
	OK      bool          `json:"ok"`
	Command string        `json:"command"`
	Data    any           `json:"data,omitempty"`
	Error   *domain.Error `json:"error,omitempty"`
}

// ItemDetail bundles an Item with its chronologically ordered Comments.
type ItemDetail struct {
	Item     domain.Item      `json:"item"`
	Comments []domain.Comment `json:"comments"`
}

// Server wraps the application Service and embedded frontend assets as an http.Handler.
type Server struct {
	svc     *app.Service
	timeout time.Duration
	distFS  fs.FS
}

// New constructs an HTTP Server for the given application Service and per-request timeout.
func New(svc *app.Service, timeout time.Duration) *Server {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	subFS, err := fs.Sub(web.Assets, "dist")
	if err != nil {
		subFS = web.Assets
	}
	return &Server{
		svc:     svc,
		timeout: timeout,
		distFS:  subFS,
	}
}

// ListenAndServe binds to addr, invokes onListen with the bound address, and serves HTTP until ctx is cancelled.
func ListenAndServe(
	ctx context.Context,
	addr string,
	svc *app.Service,
	timeout time.Duration,
	onListen func(net.Addr),
) error {
	if err := ctx.Err(); err != nil {
		return domain.AsError("serve", err)
	}
	trimmedAddr := strings.TrimSpace(addr)
	if trimmedAddr == "" {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"serve",
			"Listen address (--listen) must not be empty.",
			"Pass --listen 127.0.0.1:8080 or set AUTOMAIN_LISTEN_ADDR.",
			nil,
		)
	}
	_, portStr, err := net.SplitHostPort(trimmedAddr)
	if err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"serve",
			fmt.Sprintf("Invalid listen address %q: %v.", trimmedAddr, err),
			"Pass a host:port address such as --listen 127.0.0.1:8080.",
			err,
		)
	}
	portNum, portErr := strconv.Atoi(portStr)
	if portErr != nil || portNum < 0 || portNum > 65535 {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			"serve",
			fmt.Sprintf("Invalid listen port %q in %q; port must be an integer between 0 and 65535.", portStr, trimmedAddr),
			"Pass a valid TCP port such as --listen 127.0.0.1:8080.",
			portErr,
		)
	}

	ln, err := net.Listen("tcp", trimmedAddr)
	if err != nil {
		return domain.NewError(
			domain.ErrorCodeInternal,
			"serve",
			fmt.Sprintf("Failed to listen on %q: %v", trimmedAddr, err),
			"Verify the listen address is valid and the port is not already in use.",
			err,
		)
	}

	if onListen != nil {
		onListen(ln.Addr())
	}

	handler := New(svc, timeout)
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	errCh := make(chan error, 1)
	go func() {
		serveErr := httpSrv.Serve(ln)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if shutErr := httpSrv.Shutdown(shutCtx); shutErr != nil {
			_ = httpSrv.Close()
		}
		<-errCh
		return nil
	case serveErr := <-errCh:
		if serveErr != nil {
			return domain.NewError(
				domain.ErrorCodeInternal,
				"serve",
				fmt.Sprintf("HTTP server terminated with error: %v", serveErr),
				"Inspect server logs and port bindings.",
				serveErr,
			)
		}
		return nil
	}
}

// ServeHTTP routes API requests under /api/ and /healthz, and serves embedded static UI assets for all other paths.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")

	cleanPath := path.Clean("/" + strings.TrimSpace(r.URL.Path))
	if cleanPath == "/healthz" {
		s.handleHealth(w, r)
		return
	}

	if cleanPath == "/api" || strings.HasPrefix(cleanPath, "/api/") {
		s.serveAPI(w, r, cleanPath)
		return
	}

	s.serveStaticUI(w, r, cleanPath)
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, cleanPath string) {
	if s.svc == nil {
		writeAPIError(w, "serve", domain.NewError(
			domain.ErrorCodeNotConfigured,
			"serve",
			"Application service is not initialized.",
			"Verify server startup configuration.",
			nil,
		))
		return
	}

	reqCtx := r.Context()
	var cancel context.CancelFunc
	if s.timeout > 0 {
		reqCtx, cancel = context.WithTimeout(reqCtx, s.timeout)
		defer cancel()
	}

	subPath := strings.TrimPrefix(cleanPath, "/api")
	if subPath == "/v1" || strings.HasPrefix(subPath, "/v1/") {
		subPath = strings.TrimPrefix(subPath, "/v1")
	}
	if subPath == "" {
		subPath = "/"
	}

	switch {
	case subPath == "/health":
		s.handleHealth(w, r)

	case subPath == "/version":
		if !requireMethod(w, r, "version", http.MethodGet, http.MethodHead) {
			return
		}
		writeAPISuccess(w, http.StatusOK, "version", map[string]string{"version": app.Version})

	case subPath == "/status":
		if !requireMethod(w, r, "status", http.MethodGet, http.MethodHead) {
			return
		}
		rep, err := s.svc.Status(reqCtx)
		if err != nil {
			writeAPIError(w, "status", err)
			return
		}
		writeAPISuccess(w, http.StatusOK, "status", rep)

	case subPath == "/repositories":
		if !requireMethod(w, r, "repositories.list", http.MethodGet, http.MethodHead) {
			return
		}
		repos, err := s.svc.ListRepositories(reqCtx)
		if err != nil {
			writeAPIError(w, "repositories.list", err)
			return
		}
		if repos == nil {
			repos = []domain.SyncWatermark{}
		}
		writeAPISuccess(w, http.StatusOK, "repositories.list", repos)

	case subPath == "/stats":
		if !requireMethod(w, r, "stats", http.MethodGet, http.MethodHead) {
			return
		}
		repo := queryRepo(r)
		stats, err := s.svc.Stats(reqCtx, repo)
		if err != nil {
			writeAPIError(w, "stats", err)
			return
		}
		writeAPISuccess(w, http.StatusOK, "stats", stats)

	case subPath == "/sync":
		if !requireMethod(w, r, "sync", http.MethodPost) {
			return
		}
		s.handleSync(w, r, reqCtx)

	case subPath == "/analyze":
		if !requireMethod(w, r, "analyze", http.MethodPost) {
			return
		}
		s.handleAnalyze(w, r, reqCtx)

	case subPath == "/items":
		if !requireMethod(w, r, "items.list", http.MethodGet, http.MethodHead) {
			return
		}
		s.handleListItems(w, r, reqCtx)

	case subPath == "/items/show":
		if !requireMethod(w, r, "items.show", http.MethodGet, http.MethodHead) {
			return
		}
		q := r.URL.Query()
		numRaw := firstNonEmpty(q.Get("item"), q.Get("number"), q.Get("id"))
		s.handleGetItem(w, r, reqCtx, numRaw)

	case strings.HasPrefix(subPath, "/items/"):
		numRaw := strings.TrimPrefix(subPath, "/items/")
		if strings.Contains(numRaw, "/") {
			writeNotFoundAPIError(w, cleanPath)
			return
		}
		if !requireMethod(w, r, "items.show", http.MethodGet, http.MethodHead) {
			return
		}
		s.handleGetItem(w, r, reqCtx, numRaw)

	case subPath == "/threads":
		if !requireMethod(w, r, "threads.list", http.MethodGet, http.MethodHead) {
			return
		}
		s.handleListThreads(w, r, reqCtx)

	case subPath == "/threads/show":
		if !requireMethod(w, r, "threads.show", http.MethodGet, http.MethodHead) {
			return
		}
		s.handleGetThread(w, reqCtx, strings.TrimSpace(r.URL.Query().Get("id")))

	case strings.HasPrefix(subPath, "/threads/"):
		idRaw := strings.TrimPrefix(subPath, "/threads/")
		if strings.Contains(idRaw, "/") {
			writeNotFoundAPIError(w, cleanPath)
			return
		}
		if !requireMethod(w, r, "threads.show", http.MethodGet, http.MethodHead) {
			return
		}
		s.handleGetThread(w, reqCtx, idRaw)

	case subPath == "/relationships":
		if !requireMethod(w, r, "relationships.list", http.MethodGet, http.MethodHead) {
			return
		}
		s.handleListRelationships(w, r, reqCtx)

	case subPath == "/relationships/show":
		if !requireMethod(w, r, "relationships.show", http.MethodGet, http.MethodHead) {
			return
		}
		s.handleGetRelationship(w, reqCtx, strings.TrimSpace(r.URL.Query().Get("id")))

	case subPath == "/relationships/verify":
		if !requireMethod(w, r, "relationships.verify", http.MethodPost, http.MethodPatch, http.MethodPut) {
			return
		}
		s.handleVerifyRelationship(w, r, reqCtx, "")

	case strings.HasPrefix(subPath, "/relationships/"):
		tail := strings.TrimPrefix(subPath, "/relationships/")
		if strings.HasSuffix(tail, "/verify") {
			relID := strings.TrimSuffix(tail, "/verify")
			if strings.Contains(relID, "/") {
				writeNotFoundAPIError(w, cleanPath)
				return
			}
			if !requireMethod(w, r, "relationships.verify", http.MethodPost, http.MethodPatch, http.MethodPut) {
				return
			}
			s.handleVerifyRelationship(w, r, reqCtx, relID)
			return
		}
		if strings.Contains(tail, "/") {
			writeNotFoundAPIError(w, cleanPath)
			return
		}
		if !requireMethod(w, r, "relationships.show", http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPatch, http.MethodPut) {
			return
		}
		if r.Method == http.MethodPatch || r.Method == http.MethodPost || r.Method == http.MethodPut {
			s.handleVerifyRelationship(w, r, reqCtx, tail)
			return
		}
		s.handleGetRelationship(w, reqCtx, tail)

	default:
		writeNotFoundAPIError(w, cleanPath)
	}
}

func writeNotFoundAPIError(w http.ResponseWriter, cleanPath string) {
	writeAPIError(w, "serve", domain.NewError(
		domain.ErrorCodeNotFound,
		"serve",
		fmt.Sprintf("API endpoint %q was not found.", cleanPath),
		"Inspect supported endpoints under /api/status, /api/sync, /api/analyze, /api/threads, /api/relationships, /api/items, and /api/stats.",
		nil,
	))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, "health", http.MethodGet, http.MethodHead) {
		return
	}
	writeAPISuccess(w, http.StatusOK, "health", map[string]string{
		"status":  "ok",
		"version": app.Version,
	})
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	var body struct {
		Repository string `json:"repository"`
		Repo       string `json:"repo"`
		Limit      int    `json:"limit"`
		Since      string `json:"since"`
		Full       bool   `json:"full"`
	}
	if err := decodeOptionalJSONBody(r, &body); err != nil {
		writeAPIError(w, "sync", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"sync",
			fmt.Sprintf("Invalid JSON request body for sync: %v", err),
			"Pass a valid JSON object with repository, limit, since, or full fields.",
			err,
		))
		return
	}

	repo := firstNonEmpty(body.Repository, body.Repo)
	if qRepo := queryRepo(r); qRepo != "" {
		repo = qRepo
	}

	limit := body.Limit
	if qLimit := strings.TrimSpace(r.URL.Query().Get("limit")); qLimit != "" {
		parsedLimit, err := strconv.Atoi(qLimit)
		if err != nil {
			writeAPIError(w, "sync", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"sync",
				fmt.Sprintf("Invalid limit query parameter %q.", qLimit),
				"Pass an integer for limit.",
				err,
			))
			return
		}
		limit = parsedLimit
	}

	full := body.Full
	if qFull := strings.TrimSpace(r.URL.Query().Get("full")); qFull != "" {
		parsedFull, err := parseBoolParam("full", qFull)
		if err != nil {
			writeAPIError(w, "sync", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"sync",
				err.Error(),
				"Pass full=true or full=false.",
				err,
			))
			return
		}
		full = parsedFull
	}

	rawSince := strings.TrimSpace(body.Since)
	if qSince := strings.TrimSpace(r.URL.Query().Get("since")); qSince != "" {
		rawSince = qSince
	}
	var since *time.Time
	if rawSince != "" {
		parsed, err := time.Parse(time.RFC3339, rawSince)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339Nano, rawSince)
		}
		if err != nil {
			writeAPIError(w, "sync", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"sync",
				fmt.Sprintf("Invalid since timestamp %q; expected RFC3339 format.", rawSince),
				"Use RFC3339 format such as 2026-10-01T00:00:00Z.",
				err,
			))
			return
		}
		utc := parsed.UTC()
		since = &utc
	}

	res, err := s.svc.Sync(ctx, domain.SyncOptions{
		Repository: repo,
		Limit:      limit,
		Since:      since,
		Full:       full,
	})
	if err != nil {
		writeAPIError(w, "sync", err)
		return
	}
	writeAPISuccess(w, http.StatusOK, "sync", res)
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	var body struct {
		Repository string `json:"repository"`
		Repo       string `json:"repo"`
		ItemNumber *int   `json:"item_number"`
		Item       *int   `json:"item"`
		Limit      int    `json:"limit"`
		Reanalyze  bool   `json:"reanalyze"`
		Finder     string `json:"finder"`
	}
	if err := decodeOptionalJSONBody(r, &body); err != nil {
		writeAPIError(w, "analyze", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			fmt.Sprintf("Invalid JSON request body for analyze: %v", err),
			"Pass a valid JSON object with repository, item_number, limit, reanalyze, or finder fields.",
			err,
		))
		return
	}

	repo := firstNonEmpty(body.Repository, body.Repo)
	if qRepo := queryRepo(r); qRepo != "" {
		repo = qRepo
	}

	itemNum := 0
	itemProvided := false
	if body.ItemNumber != nil {
		itemNum = *body.ItemNumber
		itemProvided = true
	} else if body.Item != nil {
		itemNum = *body.Item
		itemProvided = true
	}
	if qItem := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("item"), r.URL.Query().Get("item_number"))); qItem != "" {
		parsedItem, err := strconv.Atoi(qItem)
		if err != nil {
			writeAPIError(w, "analyze", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"analyze",
				fmt.Sprintf("Invalid item number %q.", qItem),
				"Pass a positive integer for item.",
				err,
			))
			return
		}
		itemNum = parsedItem
		itemProvided = true
	}
	if itemNum < 0 || (itemProvided && itemNum <= 0) {
		writeAPIError(w, "analyze", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			"Item number must be a positive integer.",
			"Pass a positive GitHub item number.",
			nil,
		))
		return
	}

	limit := body.Limit
	if qLimit := strings.TrimSpace(r.URL.Query().Get("limit")); qLimit != "" {
		parsedLimit, err := strconv.Atoi(qLimit)
		if err != nil {
			writeAPIError(w, "analyze", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"analyze",
				fmt.Sprintf("Invalid limit query parameter %q.", qLimit),
				"Pass an integer for limit.",
				err,
			))
			return
		}
		limit = parsedLimit
	}

	reanalyze := body.Reanalyze
	if qRe := strings.TrimSpace(r.URL.Query().Get("reanalyze")); qRe != "" {
		parsedRe, err := parseBoolParam("reanalyze", qRe)
		if err != nil {
			writeAPIError(w, "analyze", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"analyze",
				err.Error(),
				"Pass reanalyze=true or reanalyze=false.",
				err,
			))
			return
		}
		reanalyze = parsedRe
	}

	finder := strings.TrimSpace(body.Finder)
	if qFinder := strings.TrimSpace(r.URL.Query().Get("finder")); qFinder != "" {
		finder = qFinder
	}

	res, err := s.svc.Analyze(ctx, domain.AnalyzeOptions{
		Repository: repo,
		ItemNumber: itemNum,
		Limit:      limit,
		Reanalyze:  reanalyze,
		Finder:     finder,
	})
	if err != nil {
		writeAPIError(w, "analyze", err)
		return
	}
	writeAPISuccess(w, http.StatusOK, "analyze", res)
}

func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	q := r.URL.Query()
	repo := queryRepo(r)
	kind := domain.ItemKind(strings.TrimSpace(q.Get("kind")))
	state := domain.ItemState(strings.TrimSpace(q.Get("state")))

	var dirtyPtr *bool
	if qDirty := strings.TrimSpace(firstNonEmpty(q.Get("dirty_only"), q.Get("dirty"))); qDirty != "" {
		parsed, err := parseBoolParam("dirty", qDirty)
		if err != nil {
			writeAPIError(w, "items.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.list",
				err.Error(),
				"Pass dirty=true or dirty=false.",
				err,
			))
			return
		}
		dirtyPtr = &parsed
	}

	limit := 0
	if qLimit := strings.TrimSpace(q.Get("limit")); qLimit != "" {
		parsed, err := strconv.Atoi(qLimit)
		if err != nil {
			writeAPIError(w, "items.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"items.list",
				fmt.Sprintf("Invalid limit query parameter %q.", qLimit),
				"Pass a non-negative integer for limit.",
				err,
			))
			return
		}
		limit = parsed
	}

	items, err := s.svc.ListItems(ctx, domain.ItemFilter{
		Repository: repo,
		Kind:       kind,
		State:      state,
		DirtyOnly:  dirtyPtr != nil && *dirtyPtr,
		Dirty:      dirtyPtr,
		Limit:      limit,
	})
	if err != nil {
		writeAPIError(w, "items.list", err)
		return
	}
	if items == nil {
		items = []domain.Item{}
	}
	writeAPISuccess(w, http.StatusOK, "items.list", items)
}

func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request, ctx context.Context, numRaw string) {
	num, err := strconv.Atoi(strings.TrimSpace(numRaw))
	if err != nil || num <= 0 {
		writeAPIError(w, "items.show", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"items.show",
			fmt.Sprintf("Invalid item number %q; expected a positive integer.", numRaw),
			"Pass a positive GitHub item number in /api/items/<number>.",
			err,
		))
		return
	}
	item, comments, err := s.svc.GetItem(ctx, queryRepo(r), num)
	if err != nil {
		writeAPIError(w, "items.show", err)
		return
	}
	if comments == nil {
		comments = []domain.Comment{}
	}
	writeAPISuccess(w, http.StatusOK, "items.show", ItemDetail{
		Item:     item,
		Comments: comments,
	})
}

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	q := r.URL.Query()
	repo := queryRepo(r)
	status := domain.ThreadStatus(strings.TrimSpace(firstNonEmpty(q.Get("status"), q.Get("state"))))
	threads, err := s.svc.ListThreads(ctx, domain.ThreadFilter{
		Repository: repo,
		Status:     status,
	})
	if err != nil {
		writeAPIError(w, "threads.list", err)
		return
	}
	if threads == nil {
		threads = []domain.Thread{}
	}
	writeAPISuccess(w, http.StatusOK, "threads.list", threads)
}

func (s *Server) handleGetThread(w http.ResponseWriter, ctx context.Context, idRaw string) {
	detail, err := s.svc.GetThread(ctx, domain.ThreadID(strings.TrimSpace(idRaw)))
	if err != nil {
		writeAPIError(w, "threads.show", err)
		return
	}
	if detail.Items == nil {
		detail.Items = []domain.Item{}
	}
	if detail.Relationships == nil {
		detail.Relationships = []domain.Relationship{}
	}
	writeAPISuccess(w, http.StatusOK, "threads.show", detail)
}

func (s *Server) handleListRelationships(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	q := r.URL.Query()
	repo := queryRepo(r)
	threadID := domain.ThreadID(strings.TrimSpace(firstNonEmpty(q.Get("thread"), q.Get("thread_id"))))
	relType := domain.RelationshipType(strings.TrimSpace(q.Get("type")))

	itemNum := 0
	if qItem := strings.TrimSpace(firstNonEmpty(q.Get("item"), q.Get("item_number"))); qItem != "" {
		parsed, err := strconv.Atoi(qItem)
		if err != nil || parsed <= 0 {
			writeAPIError(w, "relationships.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.list",
				"Item number (--item) must be a positive integer.",
				"Pass a positive GitHub item number.",
				err,
			))
			return
		}
		itemNum = parsed
	}

	var verifiedPtr *bool
	if qVer := strings.TrimSpace(q.Get("verified")); qVer != "" {
		parsed, err := parseBoolParam("verified", qVer)
		if err != nil {
			writeAPIError(w, "relationships.list", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.list",
				fmt.Sprintf("Invalid verified value %q; expected true or false.", qVer),
				"Pass verified=true or verified=false.",
				err,
			))
			return
		}
		verifiedPtr = &parsed
	}

	rels, err := s.svc.ListRelationships(ctx, domain.RelationshipFilter{
		Repository: repo,
		ThreadID:   threadID,
		ItemNumber: itemNum,
		Type:       relType,
		Verified:   verifiedPtr,
	})
	if err != nil {
		writeAPIError(w, "relationships.list", err)
		return
	}
	if rels == nil {
		rels = []domain.Relationship{}
	}
	writeAPISuccess(w, http.StatusOK, "relationships.list", rels)
}

func (s *Server) handleGetRelationship(w http.ResponseWriter, ctx context.Context, idRaw string) {
	rel, err := s.svc.GetRelationship(ctx, domain.RelationshipID(strings.TrimSpace(idRaw)))
	if err != nil {
		writeAPIError(w, "relationships.show", err)
		return
	}
	writeAPISuccess(w, http.StatusOK, "relationships.show", rel)
}

func (s *Server) handleVerifyRelationship(w http.ResponseWriter, r *http.Request, ctx context.Context, pathID string) {
	var body struct {
		ID       string `json:"id"`
		Verified *bool  `json:"verified"`
		Unverify *bool  `json:"unverify"`
	}
	if err := decodeOptionalJSONBody(r, &body); err != nil {
		writeAPIError(w, "relationships.verify", domain.NewError(
			domain.ErrorCodeInvalidInput,
			"relationships.verify",
			fmt.Sprintf("Invalid JSON request body for verify: %v", err),
			"Pass a valid JSON object with id and verified fields.",
			err,
		))
		return
	}

	relID := strings.TrimSpace(pathID)
	if relID == "" {
		relID = strings.TrimSpace(body.ID)
	}
	if relID == "" {
		relID = strings.TrimSpace(r.URL.Query().Get("id"))
	}

	verified := true
	if body.Verified != nil {
		verified = *body.Verified
	} else if body.Unverify != nil {
		verified = !*body.Unverify
	}
	if qVer := strings.TrimSpace(r.URL.Query().Get("verified")); qVer != "" {
		parsed, err := parseBoolParam("verified", qVer)
		if err != nil {
			writeAPIError(w, "relationships.verify", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.verify",
				err.Error(),
				"Pass verified=true or verified=false.",
				err,
			))
			return
		}
		verified = parsed
	} else if qUnver := strings.TrimSpace(r.URL.Query().Get("unverify")); qUnver != "" {
		parsed, err := parseBoolParam("unverify", qUnver)
		if err != nil {
			writeAPIError(w, "relationships.verify", domain.NewError(
				domain.ErrorCodeInvalidInput,
				"relationships.verify",
				err.Error(),
				"Pass unverify=true or unverify=false.",
				err,
			))
			return
		}
		verified = !parsed
	}

	rel, err := s.svc.VerifyRelationship(ctx, domain.RelationshipID(relID), verified)
	if err != nil {
		writeAPIError(w, "relationships.verify", err)
		return
	}
	writeAPISuccess(w, http.StatusOK, "relationships.verify", rel)
}

func (s *Server) serveStaticUI(w http.ResponseWriter, r *http.Request, cleanPath string) {
	if !requireMethod(w, r, "serve", http.MethodGet, http.MethodHead) {
		return
	}

	relPath := strings.TrimPrefix(cleanPath, "/")
	if relPath == "" {
		relPath = "index.html"
	}

	data, contentType, err := s.readEmbeddedAsset(relPath)
	if err != nil {
		// SPA fallback: serve index.html for routes without a file extension outside /assets/.
		if path.Ext(relPath) == "" && !strings.HasPrefix(relPath, "assets/") {
			relPath = "index.html"
			data, contentType, err = s.readEmbeddedAsset(relPath)
		}
		if err != nil {
			writeAPIError(w, "serve", domain.NewError(
				domain.ErrorCodeNotFound,
				"serve",
				fmt.Sprintf("Resource %q was not found.", cleanPath),
				"Verify the requested path or rebuild embedded frontend assets.",
				err,
			))
			return
		}
	}

	w.Header().Set("Content-Type", contentType)
	if relPath == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(relPath, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (s *Server) readEmbeddedAsset(relPath string) ([]byte, string, error) {
	f, err := s.distFS.Open(relPath)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		_ = f.Close()
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if info.IsDir() {
		return nil, "", fs.ErrNotExist
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", err
	}

	ext := strings.ToLower(path.Ext(relPath))
	contentType := mime.TypeByExtension(ext)
	switch ext {
	case ".html":
		contentType = "text/html; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	case ".js", ".mjs":
		contentType = "application/javascript; charset=utf-8"
	case ".json":
		contentType = "application/json; charset=utf-8"
	case ".svg":
		contentType = "image/svg+xml"
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	return data, contentType, nil
}

func requireMethod(w http.ResponseWriter, r *http.Request, operation string, allowed ...string) bool {
	for _, m := range allowed {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeAPIErrorWithStatus(w, http.StatusMethodNotAllowed, operation, domain.NewError(
		domain.ErrorCodeInvalidInput,
		operation,
		fmt.Sprintf("HTTP method %s is not allowed for %s.", r.Method, r.URL.Path),
		fmt.Sprintf("Use %s for this endpoint.", strings.Join(allowed, " or ")),
		nil,
	))
	return false
}

func decodeOptionalJSONBody(r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxRequestBodyBytes {
		return fmt.Errorf("request body exceeds maximum size of %d bytes", maxRequestBodyBytes)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	return json.Unmarshal([]byte(trimmed), dst)
}

func queryRepo(r *http.Request) string {
	q := r.URL.Query()
	return strings.TrimSpace(firstNonEmpty(q.Get("repo"), q.Get("repository")))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseBoolParam(name, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("Invalid %s value %q; expected true or false.", name, raw)
	}
}

func writeAPISuccess(w http.ResponseWriter, statusCode int, command string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(Envelope{
		OK:      true,
		Command: command,
		Data:    data,
	})
}

func writeAPIError(w http.ResponseWriter, command string, err error) {
	domErr := domain.AsError(command, err)
	writeAPIErrorWithStatus(w, httpStatusForError(domErr), command, domErr)
}

func writeAPIErrorWithStatus(w http.ResponseWriter, statusCode int, command string, domErr *domain.Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(Envelope{
		OK:      false,
		Command: command,
		Error:   domErr,
	})
}

func httpStatusForError(err *domain.Error) int {
	if err == nil {
		return http.StatusOK
	}
	switch err.Code {
	case domain.ErrorCodeInvalidInput:
		return http.StatusBadRequest
	case domain.ErrorCodeNotFound:
		return http.StatusNotFound
	case domain.ErrorCodeNotConfigured:
		return http.StatusServiceUnavailable
	case domain.ErrorCodeExternalUnavailable:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}
