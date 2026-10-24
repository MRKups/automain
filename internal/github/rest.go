package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

type flexID string

func (f *flexID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*f = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*f = flexID(strings.TrimSpace(s))
		return nil
	}
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&num); err != nil {
		return err
	}
	*f = flexID(num.String())
	return nil
}

func (f flexID) String() string {
	return string(f)
}

type restUser struct {
	Login string `json:"login"`
}

func (u *restUser) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		u.Login = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		u.Login = strings.TrimSpace(s)
		return nil
	}
	var obj struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return err
	}
	u.Login = strings.TrimSpace(obj.Login)
	return nil
}

type restLabel struct {
	Name string `json:"name"`
}

func (l *restLabel) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		l.Name = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		l.Name = strings.TrimSpace(s)
		return nil
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return err
	}
	l.Name = strings.TrimSpace(obj.Name)
	return nil
}

type restPullRequestRef struct {
	URL              string  `json:"url"`
	HTMLURL          string  `json:"html_url"`
	MergedAt         *string `json:"merged_at"`
	HasMergedAtField bool    `json:"-"`
}

func (p *restPullRequestRef) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawMap); err != nil {
		return err
	}
	if _, exists := rawMap["merged_at"]; exists {
		p.HasMergedAtField = true
	}
	var aux struct {
		URL      string  `json:"url"`
		HTMLURL  string  `json:"html_url"`
		MergedAt *string `json:"merged_at"`
	}
	if err := json.Unmarshal(trimmed, &aux); err != nil {
		return err
	}
	p.URL = aux.URL
	p.HTMLURL = aux.HTMLURL
	p.MergedAt = aux.MergedAt
	return nil
}

type restIssue struct {
	ID            flexID              `json:"id"`
	NodeID        string              `json:"node_id"`
	Number        int                 `json:"number"`
	Kind          string              `json:"kind"`
	Title         string              `json:"title"`
	Body          *string             `json:"body"`
	State         string              `json:"state"`
	HTMLURL       string              `json:"html_url"`
	URL           string              `json:"url"`
	User          *restUser           `json:"user"`
	Author        *restUser           `json:"author"`
	Labels        []restLabel         `json:"labels"`
	CommentsCount *int                `json:"comments"`
	CreatedAt     string              `json:"created_at"`
	UpdatedAt     string              `json:"updated_at"`
	MergedAt      *string             `json:"merged_at"`
	Merged        *bool               `json:"merged"`
	PullRequest   *restPullRequestRef `json:"pull_request"`
}

func (r restIssue) authorLogin() string {
	if r.User != nil && strings.TrimSpace(r.User.Login) != "" {
		return normalizeAuthor(r.User.Login)
	}
	if r.Author != nil && strings.TrimSpace(r.Author.Login) != "" {
		return normalizeAuthor(r.Author.Login)
	}
	return "ghost"
}

func (r restIssue) bodyText() string {
	if r.Body == nil {
		return ""
	}
	return *r.Body
}

func (r restIssue) externalID() string {
	if trimmed := strings.TrimSpace(r.NodeID); trimmed != "" {
		return trimmed
	}
	return r.ID.String()
}

type restComment struct {
	ID        flexID    `json:"id"`
	CommentID flexID    `json:"comment_id"`
	NodeID    string    `json:"node_id"`
	Body      *string   `json:"body"`
	HTMLURL   string    `json:"html_url"`
	URL       string    `json:"url"`
	User      *restUser `json:"user"`
	Author    *restUser `json:"author"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

func (c restComment) commentID(itemNumber int, idx int) string {
	if s := strings.TrimSpace(c.ID.String()); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.CommentID.String()); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.NodeID); s != "" {
		return s
	}
	return fmt.Sprintf("comment-%d-%d", itemNumber, idx)
}

func (c restComment) authorLogin() string {
	if c.User != nil && strings.TrimSpace(c.User.Login) != "" {
		return normalizeAuthor(c.User.Login)
	}
	if c.Author != nil && strings.TrimSpace(c.Author.Login) != "" {
		return normalizeAuthor(c.Author.Login)
	}
	return "ghost"
}

func (c restComment) bodyText() string {
	if c.Body == nil {
		return ""
	}
	return *c.Body
}

type restPullDetail struct {
	State    string  `json:"state"`
	Merged   bool    `json:"merged"`
	MergedAt *string `json:"merged_at"`
}

func (c *Client) fetchIssuesAndPRs(
	ctx context.Context,
	repo, owner, repoName string,
	since *time.Time,
	limit int,
) ([]candidateItem, error) {
	perPage := defaultPageSize
	if limit > 0 && limit < perPage {
		perPage = limit
	}

	var sinceCutoff time.Time
	if since != nil && !since.IsZero() {
		sinceCutoff = since.UTC().Truncate(time.Second)
	}

	var (
		candidates  []candidateItem
		seenNumbers = make(map[int]struct{})
		page        = 1
		nextURL     string
	)

	for {
		if err := ctx.Err(); err != nil {
			return nil, domain.AsError("sync", err)
		}

		reqURL := nextURL
		if reqURL == "" {
			q := url.Values{}
			q.Set("state", "all")
			q.Set("sort", "updated")
			q.Set("direction", "desc")
			q.Set("per_page", strconv.Itoa(perPage))
			q.Set("page", strconv.Itoa(page))
			if !sinceCutoff.IsZero() {
				q.Set("since", sinceCutoff.Format(time.RFC3339))
			}
			reqURL = fmt.Sprintf(
				"%s/repos/%s/%s/issues?%s",
				c.restBaseURL,
				url.PathEscape(owner),
				url.PathEscape(repoName),
				q.Encode(),
			)
		}

		bodyBytes, headers, statusCode, err := c.doRESTGet(ctx, reqURL, repo, false)
		if err != nil {
			return nil, err
		}
		_ = statusCode

		var rawIssues []restIssue
		if err := json.Unmarshal(bodyBytes, &rawIssues); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("Failed to decode GitHub issues response for %q: %v", repo, err),
				"Verify GitHub API availability and response format.",
				err,
			)
		}

		if len(rawIssues) == 0 {
			break
		}

		newOnPage := 0
		reachedSinceBoundary := false

		for _, raw := range rawIssues {
			if raw.Number <= 0 {
				continue
			}

			createdAt, err := parseGitHubTime(raw.CreatedAt)
			if err != nil {
				return nil, domain.NewError(
					domain.ErrorCodeExternalUnavailable,
					"sync",
					fmt.Sprintf("Invalid created_at timestamp on %s#%d: %v", repo, raw.Number, err),
					"Verify GitHub API timestamp formatting.",
					err,
				)
			}
			updatedAt, err := parseGitHubTime(raw.UpdatedAt)
			if err != nil {
				return nil, domain.NewError(
					domain.ErrorCodeExternalUnavailable,
					"sync",
					fmt.Sprintf("Invalid updated_at timestamp on %s#%d: %v", repo, raw.Number, err),
					"Verify GitHub API timestamp formatting.",
					err,
				)
			}
			if updatedAt.IsZero() && !createdAt.IsZero() {
				updatedAt = createdAt
			}

			if !sinceCutoff.IsZero() && !updatedAt.IsZero() && updatedAt.Before(sinceCutoff) {
				reachedSinceBoundary = true
				continue
			}

			if _, exists := seenNumbers[raw.Number]; exists {
				continue
			}
			seenNumbers[raw.Number] = struct{}{}
			newOnPage++

			cand := toIssueOrPRCandidate(repo, raw, createdAt, updatedAt)
			candidates = append(candidates, cand)
		}

		if limit > 0 && len(candidates) >= limit {
			break
		}
		if reachedSinceBoundary || newOnPage == 0 {
			break
		}

		linkHeader := strings.Join(headers.Values("Link"), ", ")
		if strings.TrimSpace(linkHeader) != "" {
			parsedNext := parseNextLink(linkHeader, reqURL)
			if parsedNext == "" {
				break
			}
			nextURL = parsedNext
			page++
			continue
		}

		if len(rawIssues) < perPage {
			break
		}
		nextURL = ""
		page++
	}

	return candidates, nil
}

func toIssueOrPRCandidate(repo string, raw restIssue, createdAt, updatedAt time.Time) candidateItem {
	isPR := raw.PullRequest != nil || strings.EqualFold(raw.Kind, string(domain.ItemKindPullRequest))
	kind := domain.ItemKindIssue
	if isPR {
		kind = domain.ItemKindPullRequest
	}

	state := domain.ItemStateOpen
	needsPRMergeCheck := false

	if isPR {
		merged := false
		if raw.PullRequest != nil && raw.PullRequest.MergedAt != nil && strings.TrimSpace(*raw.PullRequest.MergedAt) != "" {
			merged = true
		} else if raw.MergedAt != nil && strings.TrimSpace(*raw.MergedAt) != "" {
			merged = true
		} else if raw.Merged != nil && *raw.Merged {
			merged = true
		} else if strings.EqualFold(raw.State, string(domain.ItemStateMerged)) {
			merged = true
		}

		if merged {
			state = domain.ItemStateMerged
		} else if strings.EqualFold(raw.State, string(domain.ItemStateOpen)) || strings.TrimSpace(raw.State) == "" {
			state = domain.ItemStateOpen
		} else {
			state = domain.ItemStateClosed
			hasExplicitMergedField := (raw.PullRequest != nil && raw.PullRequest.HasMergedAtField) ||
				raw.MergedAt != nil ||
				raw.Merged != nil
			if !hasExplicitMergedField {
				needsPRMergeCheck = true
			}
		}
	} else {
		if strings.EqualFold(raw.State, string(domain.ItemStateClosed)) {
			state = domain.ItemStateClosed
		} else {
			state = domain.ItemStateOpen
		}
	}

	labels := make([]string, 0, len(raw.Labels))
	for _, l := range raw.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}

	itemURL := strings.TrimSpace(raw.HTMLURL)
	if itemURL == "" && raw.PullRequest != nil {
		itemURL = strings.TrimSpace(raw.PullRequest.HTMLURL)
	}
	if itemURL == "" {
		itemURL = strings.TrimSpace(raw.URL)
	}
	if itemURL == "" {
		pathSeg := "issues"
		if isPR {
			pathSeg = "pull"
		}
		itemURL = fmt.Sprintf("https://github.com/%s/%s/%d", repo, pathSeg, raw.Number)
	}

	return candidateItem{
		item: domain.Item{
			Repository: repo,
			Number:     raw.Number,
			ExternalID: raw.externalID(),
			Kind:       kind,
			State:      state,
			Title:      normalizeTitle(raw.Title, kind, raw.Number),
			Body:       raw.bodyText(),
			Author:     raw.authorLogin(),
			Labels:     domain.NormalizeLabels(labels),
			URL:        itemURL,
			CreatedAt:  createdAt,
			UpdatedAt:  updatedAt,
		},
		commentsCount:     raw.CommentsCount,
		needsPRMergeCheck: needsPRMergeCheck,
	}
}

func (c *Client) checkPullRequestMerged(ctx context.Context, repo, owner, repoName string, number int) (bool, error) {
	reqURL := fmt.Sprintf(
		"%s/repos/%s/%s/pulls/%d",
		c.restBaseURL,
		url.PathEscape(owner),
		url.PathEscape(repoName),
		number,
	)
	bodyBytes, _, statusCode, err := c.doRESTGet(ctx, reqURL, repo, true)
	if err != nil {
		return false, err
	}
	if statusCode == http.StatusNotFound || len(bodyBytes) == 0 {
		return false, nil
	}
	var detail restPullDetail
	if err := json.Unmarshal(bodyBytes, &detail); err != nil {
		return false, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"sync",
			fmt.Sprintf("Failed to decode pull request detail for %s#%d: %v", repo, number, err),
			"Verify GitHub API availability and response format.",
			err,
		)
	}
	if detail.Merged || strings.EqualFold(detail.State, string(domain.ItemStateMerged)) {
		return true, nil
	}
	if detail.MergedAt != nil && strings.TrimSpace(*detail.MergedAt) != "" {
		return true, nil
	}
	return false, nil
}

func (c *Client) fetchIssueComments(
	ctx context.Context,
	repo, owner, repoName string,
	itemNumber int,
	allowNotFound bool,
) ([]domain.Comment, error) {
	var (
		comments       []domain.Comment
		seenCommentIDs = make(map[string]struct{})
		commentSeq     = 0
		page           = 1
		nextURL        string
	)

	for {
		if err := ctx.Err(); err != nil {
			return nil, domain.AsError("sync", err)
		}

		reqURL := nextURL
		if reqURL == "" {
			q := url.Values{}
			q.Set("per_page", strconv.Itoa(defaultPageSize))
			q.Set("page", strconv.Itoa(page))
			reqURL = fmt.Sprintf(
				"%s/repos/%s/%s/issues/%d/comments?%s",
				c.restBaseURL,
				url.PathEscape(owner),
				url.PathEscape(repoName),
				itemNumber,
				q.Encode(),
			)
		}

		bodyBytes, headers, statusCode, err := c.doRESTGet(ctx, reqURL, repo, allowNotFound && page == 1)
		if err != nil {
			return nil, err
		}
		if statusCode == http.StatusNotFound {
			return []domain.Comment{}, nil
		}

		var rawComments []restComment
		if err := json.Unmarshal(bodyBytes, &rawComments); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("Failed to decode comments for %s#%d: %v", repo, itemNumber, err),
				"Verify GitHub API availability and response format.",
				err,
			)
		}

		if len(rawComments) == 0 {
			break
		}

		newOnPage := 0
		for _, rc := range rawComments {
			commentSeq++
			cid := rc.commentID(itemNumber, commentSeq)
			if _, exists := seenCommentIDs[cid]; exists {
				continue
			}
			seenCommentIDs[cid] = struct{}{}
			newOnPage++

			createdAt, err := parseGitHubTime(rc.CreatedAt)
			if err != nil {
				return nil, domain.NewError(
					domain.ErrorCodeExternalUnavailable,
					"sync",
					fmt.Sprintf("Invalid created_at timestamp on comment %q (%s#%d): %v", cid, repo, itemNumber, err),
					"Verify GitHub API timestamp formatting.",
					err,
				)
			}
			updatedAt, err := parseGitHubTime(rc.UpdatedAt)
			if err != nil {
				return nil, domain.NewError(
					domain.ErrorCodeExternalUnavailable,
					"sync",
					fmt.Sprintf("Invalid updated_at timestamp on comment %q (%s#%d): %v", cid, repo, itemNumber, err),
					"Verify GitHub API timestamp formatting.",
					err,
				)
			}
			if updatedAt.IsZero() && !createdAt.IsZero() {
				updatedAt = createdAt
			}

			commentURL := strings.TrimSpace(rc.HTMLURL)
			if commentURL == "" {
				commentURL = strings.TrimSpace(rc.URL)
			}

			comments = append(comments, domain.Comment{
				Repository: repo,
				ItemNumber: itemNumber,
				CommentID:  cid,
				Author:     rc.authorLogin(),
				Body:       rc.bodyText(),
				URL:        commentURL,
				CreatedAt:  createdAt,
				UpdatedAt:  updatedAt,
			})
		}

		if newOnPage == 0 {
			break
		}

		linkHeader := strings.Join(headers.Values("Link"), ", ")
		if strings.TrimSpace(linkHeader) != "" {
			parsedNext := parseNextLink(linkHeader, reqURL)
			if parsedNext == "" {
				break
			}
			nextURL = parsedNext
			page++
			continue
		}

		if len(rawComments) < defaultPageSize {
			break
		}
		nextURL = ""
		page++
	}

	return comments, nil
}

func (c *Client) doRESTGet(ctx context.Context, reqURL, repo string, allowNotFound bool) ([]byte, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, nil, 0, domain.NewError(
			domain.ErrorCodeInternal,
			"sync",
			fmt.Sprintf("Failed to build GitHub API request: %v", err),
			"Verify the GitHub API URL configuration.",
			err,
		)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, 0, domain.AsError("sync", ctx.Err())
		}
		return nil, nil, 0, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"sync",
			fmt.Sprintf("GitHub API request failed for %q: %v", repo, err),
			"Check network connectivity and GitHub API availability, or increase --timeout.",
			err,
		)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if readErr != nil {
		return nil, resp.Header, resp.StatusCode, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"sync",
			fmt.Sprintf("Failed to read GitHub API response body for %q: %v", repo, readErr),
			"Retry synchronization or inspect network stability.",
			readErr,
		)
	}

	if allowNotFound && resp.StatusCode == http.StatusNotFound {
		return nil, resp.Header, resp.StatusCode, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, resp.StatusCode, mapHTTPError(resp.StatusCode, resp.Header, bodyBytes, repo, c.token != "")
	}

	return bodyBytes, resp.Header, resp.StatusCode, nil
}

func mapHTTPError(statusCode int, headers http.Header, body []byte, repo string, hasToken bool) error {
	var apiMsg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &apiMsg)
	detail := strings.TrimSpace(apiMsg.Message)

	switch statusCode {
	case http.StatusNotFound:
		msg := fmt.Sprintf("GitHub repository %q was not found (HTTP 404).", repo)
		if detail != "" {
			msg = fmt.Sprintf("GitHub repository %q was not found (HTTP 404: %s).", repo, detail)
		}
		recovery := "Verify the --repo <owner/name> slug exists on GitHub."
		if !hasToken {
			recovery = "Verify the --repo <owner/name> slug exists, or set GITHUB_TOKEN if the repository is private."
		}
		return domain.NewError(domain.ErrorCodeNotFound, "sync", msg, recovery, nil)

	case http.StatusUnauthorized:
		msg := "GitHub API authentication failed (HTTP 401)."
		if detail != "" {
			msg = fmt.Sprintf("GitHub API authentication failed (HTTP 401: %s).", detail)
		}
		return domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"sync",
			msg,
			"Verify that GITHUB_TOKEN is valid and has not expired.",
			nil,
		)

	case http.StatusForbidden, http.StatusTooManyRequests:
		if headers.Get("X-RateLimit-Remaining") == "0" || statusCode == http.StatusTooManyRequests || strings.Contains(strings.ToLower(detail), "rate limit") {
			resetHint := ""
			if resetRaw := strings.TrimSpace(headers.Get("X-RateLimit-Reset")); resetRaw != "" {
				if epoch, err := strconv.ParseInt(resetRaw, 10, 64); err == nil && epoch > 0 {
					resetHint = fmt.Sprintf(" (resets at %s)", time.Unix(epoch, 0).UTC().Format(time.RFC3339))
				}
			}
			msg := fmt.Sprintf("GitHub API rate limit exceeded for %q (HTTP %d)%s.", repo, statusCode, resetHint)
			recovery := "Wait for the rate limit window to reset or pass a smaller --limit."
			if !hasToken {
				recovery = "Set GITHUB_TOKEN to increase the GitHub API rate limit, or wait for the rate limit window to reset."
			}
			return domain.NewError(domain.ErrorCodeExternalUnavailable, "sync", msg, recovery, nil)
		}
		msg := fmt.Sprintf("GitHub API access forbidden for %q (HTTP %d).", repo, statusCode)
		if detail != "" {
			msg = fmt.Sprintf("GitHub API access forbidden for %q (HTTP %d: %s).", repo, statusCode, detail)
		}
		return domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"sync",
			msg,
			"Ensure GITHUB_TOKEN has repository read permissions.",
			nil,
		)

	default:
		msg := fmt.Sprintf("GitHub API returned unexpected HTTP %d for %q.", statusCode, repo)
		if detail != "" {
			msg = fmt.Sprintf("GitHub API returned HTTP %d for %q: %s.", statusCode, repo, detail)
		}
		return domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"sync",
			msg,
			"Retry synchronization later or verify GitHub API status.",
			nil,
		)
	}
}

func parseNextLink(linkHeader, baseURL string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		rawURL := strings.TrimSpace(urlPart[1 : len(urlPart)-1])
		if rawURL == "" {
			continue
		}
		isNext := false
		for _, param := range segments[1:] {
			p := strings.TrimSpace(param)
			if p == `rel="next"` || p == `rel=next` || p == `rel='next'` {
				isNext = true
				break
			}
		}
		if isNext {
			parsedRef, err := url.Parse(rawURL)
			if err != nil {
				return ""
			}
			parsedBase, err := url.Parse(baseURL)
			if err != nil {
				return ""
			}
			resolved := parsedBase.ResolveReference(parsedRef)
			if !strings.EqualFold(resolved.Scheme, parsedBase.Scheme) || !strings.EqualFold(resolved.Host, parsedBase.Host) {
				return ""
			}
			return resolved.String()
		}
	}
	return ""
}
