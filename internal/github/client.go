package github

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

const (
	DefaultRESTBaseURL    = "https://api.github.com"
	DefaultGraphQLURL     = "https://api.github.com/graphql"
	DefaultUserAgent      = "automain/0.1.0-dev"
	defaultHTTPTimeout    = 60 * time.Second
	defaultPageSize       = 100
	defaultCommentWorkers = 4
	maxResponseBodyBytes  = 64 << 20
)

// ClientConfig configures the GitHub REST and GraphQL ingestion client.
type ClientConfig struct {
	Token       string
	RESTBaseURL string
	GraphQLURL  string
	HTTPClient  *http.Client
	UserAgent   string
}

// Client implements domain.GitHubSource using the GitHub REST v3 and GraphQL v4 APIs.
type Client struct {
	token       string
	restBaseURL string
	graphQLURL  string
	httpClient  *http.Client
	userAgent   string
}

// NewClient constructs a GitHub ingestion client with the given personal access token.
// Optional environment overrides AUTOMAIN_GITHUB_API_URL / GITHUB_API_URL and
// AUTOMAIN_GITHUB_GRAPHQL_URL / GITHUB_GRAPHQL_URL are respected when present.
func NewClient(token string) *Client {
	restURL := strings.TrimSpace(os.Getenv("AUTOMAIN_GITHUB_API_URL"))
	if restURL == "" {
		restURL = strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	}
	graphqlURL := strings.TrimSpace(os.Getenv("AUTOMAIN_GITHUB_GRAPHQL_URL"))
	if graphqlURL == "" {
		graphqlURL = strings.TrimSpace(os.Getenv("GITHUB_GRAPHQL_URL"))
	}
	return NewClientWithConfig(ClientConfig{
		Token:       token,
		RESTBaseURL: restURL,
		GraphQLURL:  graphqlURL,
	})
}

// NewClientWithConfig constructs a GitHub client from explicit ClientConfig settings.
func NewClientWithConfig(cfg ClientConfig) *Client {
	restBase := strings.TrimRight(strings.TrimSpace(cfg.RESTBaseURL), "/")
	if restBase == "" {
		restBase = DefaultRESTBaseURL
	}

	graphqlURL := strings.TrimSpace(cfg.GraphQLURL)
	if graphqlURL == "" {
		if restBase == DefaultRESTBaseURL {
			graphqlURL = DefaultGraphQLURL
		} else if strings.HasSuffix(restBase, "/api/v3") {
			graphqlURL = strings.TrimSuffix(restBase, "/api/v3") + "/api/graphql"
		} else {
			graphqlURL = restBase + "/graphql"
		}
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: defaultHTTPTimeout,
		}
	}

	userAgent := strings.TrimSpace(cfg.UserAgent)
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	return &Client{
		token:       strings.TrimSpace(cfg.Token),
		restBaseURL: restBase,
		graphQLURL:  graphqlURL,
		httpClient:  httpClient,
		userAgent:   userAgent,
	}
}

type candidateItem struct {
	item              domain.Item
	comments          []domain.Comment
	commentsCount     *int
	needsPRMergeCheck bool
}

// FetchUpdates fetches issues, pull requests, discussions, and comments from GitHub.
func (c *Client) FetchUpdates(ctx context.Context, opts domain.SyncOptions, since *time.Time) (domain.FetchBatch, error) {
	if err := ctx.Err(); err != nil {
		return domain.FetchBatch{}, domain.AsError("sync", err)
	}

	repo := strings.TrimSpace(opts.Repository)
	owner, repoName, err := splitRepoSlug(repo)
	if err != nil {
		return domain.FetchBatch{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"sync",
			err.Error(),
			"Pass the repository in owner/name format, for example --repo cli/cli.",
			err,
		)
	}
	if opts.Limit < 0 {
		return domain.FetchBatch{}, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"sync",
			"Sync item limit (--limit) must not be negative.",
			"Pass a positive integer for --limit or omit it.",
			nil,
		)
	}

	effectiveSince := since
	if effectiveSince == nil && !opts.Full && opts.Since != nil {
		effectiveSince = opts.Since
	}
	if effectiveSince != nil && effectiveSince.IsZero() {
		effectiveSince = nil
	}

	issueCandidates, err := c.fetchIssuesAndPRs(ctx, repo, owner, repoName, effectiveSince, opts.Limit)
	if err != nil {
		return domain.FetchBatch{}, err
	}

	var (
		discussionCandidates []candidateItem
		discussionsSynced    bool
		warnings             []string
	)

	if c.token == "" {
		discussionsSynced = false
		warnings = append(warnings, "GITHUB_TOKEN is not set; skipped GitHub Discussions sync (GraphQL requires authentication).")
	} else {
		discItems, synced, warnMsgs, discErr := c.fetchDiscussions(ctx, repo, owner, repoName, effectiveSince, opts.Limit)
		if discErr != nil {
			return domain.FetchBatch{}, discErr
		}
		discussionCandidates = discItems
		discussionsSynced = synced
		warnings = append(warnings, warnMsgs...)
	}

	merged := mergeAndLimitCandidates(issueCandidates, discussionCandidates, opts.Limit)

	if err := c.hydrateCandidateDetails(ctx, repo, owner, repoName, merged); err != nil {
		return domain.FetchBatch{}, err
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].item.Number < merged[j].item.Number
	})

	items := make([]domain.Item, len(merged))
	seenCommentOwner := make(map[string]int)
	var allComments []domain.Comment

	for i := range merged {
		cand := &merged[i]
		localSeen := make(map[string]struct{}, len(cand.comments))
		normalizedComments := make([]domain.Comment, 0, len(cand.comments))

		for _, cm := range cand.comments {
			cid := strings.TrimSpace(cm.CommentID)
			if cid == "" {
				continue
			}
			if existingItemNum, used := seenCommentOwner[cid]; used && existingItemNum != cand.item.Number {
				cid = fmt.Sprintf("%d-%s", cand.item.Number, cid)
			}
			if _, exists := localSeen[cid]; exists {
				continue
			}
			localSeen[cid] = struct{}{}
			seenCommentOwner[cid] = cand.item.Number
			cm.CommentID = cid
			cm.Repository = repo
			cm.ItemNumber = cand.item.Number
			cm.Author = normalizeAuthor(cm.Author)
			normalizedComments = append(normalizedComments, cm)
			allComments = append(allComments, cm)
		}

		cand.comments = normalizedComments
		cand.item.Repository = repo
		cand.item.Labels = domain.NormalizeLabels(cand.item.Labels)
		cand.item.ContentHash = ComputeContentHash(cand.item, cand.comments)
		items[i] = cand.item
	}

	sort.Slice(allComments, func(i, j int) bool {
		if allComments[i].ItemNumber != allComments[j].ItemNumber {
			return allComments[i].ItemNumber < allComments[j].ItemNumber
		}
		ti := allComments[i].CreatedAt.UTC()
		tj := allComments[j].CreatedAt.UTC()
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return allComments[i].CommentID < allComments[j].CommentID
	})

	if allComments == nil {
		allComments = []domain.Comment{}
	}

	return domain.FetchBatch{
		Items:             items,
		Comments:          allComments,
		DiscussionsSynced: discussionsSynced,
		Warnings:          warnings,
	}, nil
}

func mergeAndLimitCandidates(issues, discussions []candidateItem, limit int) []candidateItem {
	byNumber := make(map[int]candidateItem, len(issues)+len(discussions))
	for _, cand := range issues {
		byNumber[cand.item.Number] = cand
	}
	for _, cand := range discussions {
		existing, exists := byNumber[cand.item.Number]
		if !exists || !cand.item.UpdatedAt.Before(existing.item.UpdatedAt) {
			byNumber[cand.item.Number] = cand
		}
	}

	merged := make([]candidateItem, 0, len(byNumber))
	for _, cand := range byNumber {
		merged = append(merged, cand)
	}

	sort.Slice(merged, func(i, j int) bool {
		ti := merged[i].item.UpdatedAt.UTC()
		tj := merged[j].item.UpdatedAt.UTC()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return merged[i].item.Number > merged[j].item.Number
	})

	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func (c *Client) hydrateCandidateDetails(ctx context.Context, repo, owner, repoName string, candidates []candidateItem) error {
	if len(candidates) == 0 {
		return nil
	}

	workIndices := make([]int, 0, len(candidates))
	for i := range candidates {
		cand := &candidates[i]
		if cand.item.Kind == domain.ItemKindDiscussion {
			continue
		}
		needsComments := cand.commentsCount == nil || *cand.commentsCount > 0
		if needsComments || cand.needsPRMergeCheck {
			workIndices = append(workIndices, i)
		}
	}

	if len(workIndices) == 0 {
		return nil
	}

	workers := defaultCommentWorkers
	if len(workIndices) < workers {
		workers = len(workIndices)
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int, len(workIndices))
	for _, idx := range workIndices {
		jobs <- idx
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if workCtx.Err() != nil {
					return
				}
				cand := &candidates[idx]
				if cand.needsPRMergeCheck {
					merged, err := c.checkPullRequestMerged(workCtx, repo, owner, repoName, cand.item.Number)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
							cancel()
						}
						mu.Unlock()
						return
					}
					if merged {
						cand.item.State = domain.ItemStateMerged
					}
				}
				if cand.commentsCount == nil || *cand.commentsCount > 0 {
					allowMissing := cand.commentsCount == nil
					comments, err := c.fetchIssueComments(workCtx, repo, owner, repoName, cand.item.Number, allowMissing)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
							cancel()
						}
						mu.Unlock()
						return
					}
					cand.comments = comments
				}
			}
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return domain.AsError("sync", err)
	}
	return nil
}
