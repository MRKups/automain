package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

const discussionsGraphQLQuery = `
query($owner: String!, $name: String!, $first: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    discussions(first: $first, after: $after, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
        id
        number
        title
        body
        url
        createdAt
        updatedAt
        closed
        isAnswered
        author {
          login
        }
        labels(first: 50) {
          nodes {
            name
          }
        }
        comments(first: 100) {
          pageInfo {
            hasNextPage
            endCursor
          }
          nodes {
            id
            body
            url
            createdAt
            updatedAt
            author {
              login
            }
            replies(first: 100) {
              nodes {
                id
                body
                url
                createdAt
                updatedAt
                author {
                  login
                }
              }
            }
          }
        }
      }
    }
  }
}
`

const discussionCommentsGraphQLQuery = `
query($owner: String!, $name: String!, $number: Int!, $first: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    discussion(number: $number) {
      comments(first: $first, after: $after) {
        pageInfo {
          hasNextPage
          endCursor
        }
        nodes {
          id
          body
          url
          createdAt
          updatedAt
          author {
            login
          }
          replies(first: 100) {
            nodes {
              id
              body
              url
              createdAt
              updatedAt
              author {
                login
              }
            }
          }
        }
      }
    }
  }
}
`

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type graphQLPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type graphQLLabelConnection struct {
	Nodes []restLabel `json:"nodes"`
	Edges []struct {
		Node restLabel `json:"node"`
	} `json:"edges"`
}

func (c graphQLLabelConnection) list() []string {
	var out []string
	for _, n := range c.Nodes {
		if n.Name != "" {
			out = append(out, n.Name)
		}
	}
	if len(out) == 0 {
		for _, e := range c.Edges {
			if e.Node.Name != "" {
				out = append(out, e.Node.Name)
			}
		}
	}
	return domain.NormalizeLabels(out)
}

type graphQLReplyNode struct {
	ID        flexID    `json:"id"`
	Body      *string   `json:"body"`
	URL       string    `json:"url"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt string    `json:"createdAt"`
	UpdatedAt string    `json:"updatedAt"`
	Author    *restUser `json:"author"`
	User      *restUser `json:"user"`
}

func (r graphQLReplyNode) authorLogin() string {
	if r.Author != nil && strings.TrimSpace(r.Author.Login) != "" {
		return normalizeAuthor(r.Author.Login)
	}
	if r.User != nil && strings.TrimSpace(r.User.Login) != "" {
		return normalizeAuthor(r.User.Login)
	}
	return "ghost"
}

func (r graphQLReplyNode) bodyText() string {
	if r.Body == nil {
		return ""
	}
	return *r.Body
}

type graphQLReplyConnection struct {
	Nodes []graphQLReplyNode `json:"nodes"`
	Edges []struct {
		Node graphQLReplyNode `json:"node"`
	} `json:"edges"`
}

func (c graphQLReplyConnection) items() []graphQLReplyNode {
	if len(c.Nodes) > 0 {
		return c.Nodes
	}
	if len(c.Edges) == 0 {
		return nil
	}
	out := make([]graphQLReplyNode, len(c.Edges))
	for i, e := range c.Edges {
		out[i] = e.Node
	}
	return out
}

type graphQLCommentNode struct {
	ID        flexID                  `json:"id"`
	Body      *string                 `json:"body"`
	URL       string                  `json:"url"`
	HTMLURL   string                  `json:"html_url"`
	CreatedAt string                  `json:"createdAt"`
	UpdatedAt string                  `json:"updatedAt"`
	Author    *restUser               `json:"author"`
	User      *restUser               `json:"user"`
	Replies   *graphQLReplyConnection `json:"replies"`
}

func (n graphQLCommentNode) authorLogin() string {
	if n.Author != nil && strings.TrimSpace(n.Author.Login) != "" {
		return normalizeAuthor(n.Author.Login)
	}
	if n.User != nil && strings.TrimSpace(n.User.Login) != "" {
		return normalizeAuthor(n.User.Login)
	}
	return "ghost"
}

func (n graphQLCommentNode) bodyText() string {
	if n.Body == nil {
		return ""
	}
	return *n.Body
}

type graphQLCommentConnection struct {
	PageInfo graphQLPageInfo      `json:"pageInfo"`
	Nodes    []graphQLCommentNode `json:"nodes"`
	Edges    []struct {
		Node graphQLCommentNode `json:"node"`
	} `json:"edges"`
}

func (c graphQLCommentConnection) items() []graphQLCommentNode {
	if len(c.Nodes) > 0 {
		return c.Nodes
	}
	if len(c.Edges) == 0 {
		return nil
	}
	out := make([]graphQLCommentNode, len(c.Edges))
	for i, e := range c.Edges {
		out[i] = e.Node
	}
	return out
}

type graphQLDiscussionNode struct {
	ID         flexID                    `json:"id"`
	Number     int                       `json:"number"`
	Title      string                    `json:"title"`
	Body       *string                   `json:"body"`
	URL        string                    `json:"url"`
	HTMLURL    string                    `json:"html_url"`
	CreatedAt  string                    `json:"createdAt"`
	UpdatedAt  string                    `json:"updatedAt"`
	Closed     bool                      `json:"closed"`
	IsAnswered bool                      `json:"isAnswered"`
	State      string                    `json:"state"`
	Answer     json.RawMessage           `json:"answer"`
	Author     *restUser                 `json:"author"`
	User       *restUser                 `json:"user"`
	Labels     *graphQLLabelConnection   `json:"labels"`
	Comments   *graphQLCommentConnection `json:"comments"`
}

func (d graphQLDiscussionNode) authorLogin() string {
	if d.Author != nil && strings.TrimSpace(d.Author.Login) != "" {
		return normalizeAuthor(d.Author.Login)
	}
	if d.User != nil && strings.TrimSpace(d.User.Login) != "" {
		return normalizeAuthor(d.User.Login)
	}
	return "ghost"
}

func (d graphQLDiscussionNode) bodyText() string {
	if d.Body == nil {
		return ""
	}
	return *d.Body
}

func (d graphQLDiscussionNode) itemState() domain.ItemState {
	hasAnswerObj := len(bytes.TrimSpace(d.Answer)) > 0 && !bytes.Equal(bytes.TrimSpace(d.Answer), []byte("null"))
	if d.IsAnswered || hasAnswerObj || strings.EqualFold(d.State, string(domain.ItemStateAnswered)) {
		return domain.ItemStateAnswered
	}
	if d.Closed || strings.EqualFold(d.State, string(domain.ItemStateClosed)) {
		return domain.ItemStateClosed
	}
	return domain.ItemStateOpen
}

type graphQLDiscussionConnection struct {
	PageInfo graphQLPageInfo         `json:"pageInfo"`
	Nodes    []graphQLDiscussionNode `json:"nodes"`
	Edges    []struct {
		Node graphQLDiscussionNode `json:"node"`
	} `json:"edges"`
}

func (c graphQLDiscussionConnection) items() []graphQLDiscussionNode {
	if len(c.Nodes) > 0 {
		return c.Nodes
	}
	if len(c.Edges) == 0 {
		return nil
	}
	out := make([]graphQLDiscussionNode, len(c.Edges))
	for i, e := range c.Edges {
		out[i] = e.Node
	}
	return out
}

type graphQLResponse struct {
	Data *struct {
		Repository *struct {
			Discussions *graphQLDiscussionConnection `json:"discussions"`
			Discussion  *struct {
				Comments *graphQLCommentConnection `json:"comments"`
			} `json:"discussion"`
		} `json:"repository"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

func (c *Client) fetchDiscussions(
	ctx context.Context,
	repo, owner, repoName string,
	since *time.Time,
	limit int,
) ([]candidateItem, bool, []string, error) {
	pageSize := defaultPageSize
	if limit > 0 && limit < pageSize {
		pageSize = limit
	}

	var sinceCutoff time.Time
	if since != nil && !since.IsZero() {
		sinceCutoff = since.UTC().Truncate(time.Second)
	}

	var (
		candidates  []candidateItem
		warnings    []string
		seenNumbers = make(map[int]struct{})
		seenCursors = make(map[string]struct{})
		afterCursor *string
	)

	for {
		if err := ctx.Err(); err != nil {
			return nil, false, nil, domain.AsError("sync", err)
		}

		vars := map[string]any{
			"owner": owner,
			"name":  repoName,
			"first": pageSize,
		}
		if afterCursor != nil && *afterCursor != "" {
			vars["after"] = *afterCursor
		} else {
			vars["after"] = nil
		}

		payloadBytes, err := json.Marshal(graphQLRequest{
			Query:     discussionsGraphQLQuery,
			Variables: vars,
		})
		if err != nil {
			return nil, false, nil, domain.NewError(
				domain.ErrorCodeInternal,
				"sync",
				fmt.Sprintf("Failed to encode GraphQL discussions request: %v", err),
				"Verify GraphQL request variables.",
				err,
			)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL, bytes.NewReader(payloadBytes))
		if err != nil {
			return nil, false, nil, domain.NewError(
				domain.ErrorCodeInternal,
				"sync",
				fmt.Sprintf("Failed to construct GraphQL request: %v", err),
				"Verify the GitHub GraphQL URL configuration.",
				err,
			)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, nil, domain.AsError("sync", ctx.Err())
			}
			return nil, false, nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("GitHub Discussions GraphQL request failed for %q: %v", repo, err),
				"Check network connectivity and GitHub GraphQL availability, or increase --timeout.",
				err,
			)
		}

		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
		drainAndCloseBody(resp.Body)

		if readErr != nil {
			return nil, false, nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("Failed to read GitHub Discussions GraphQL response for %q: %v", repo, readErr),
				"Retry synchronization or inspect network stability.",
				readErr,
			)
		}

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
			return nil, false, []string{fmt.Sprintf("Skipped GitHub Discussions sync (GraphQL endpoint returned HTTP %d).", resp.StatusCode)}, nil
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, false, nil, mapHTTPError(resp.StatusCode, resp.Header, bodyBytes, repo, c.token != "")
		}

		var gqlResp graphQLResponse
		if err := json.Unmarshal(bodyBytes, &gqlResp); err != nil {
			return nil, false, nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("Invalid GitHub Discussions GraphQL response for %q: %v", repo, err),
				"Verify GitHub GraphQL API availability and response format.",
				err,
			)
		}

		if len(gqlResp.Errors) > 0 {
			msgs := make([]string, 0, len(gqlResp.Errors))
			for _, ge := range gqlResp.Errors {
				errType := strings.ToUpper(strings.TrimSpace(ge.Type))
				trimmedMsg := strings.TrimSpace(ge.Message)
				lowerMsg := strings.ToLower(trimmedMsg)
				if errType == "RATE_LIMITED" || strings.Contains(lowerMsg, "rate limit") {
					return nil, false, nil, domain.NewError(
						domain.ErrorCodeExternalUnavailable,
						"sync",
						fmt.Sprintf("GitHub GraphQL rate limit exceeded for %q: %s", repo, trimmedMsg),
						"Wait for the GitHub GraphQL rate limit window to reset or pass a smaller --limit.",
						nil,
					)
				}
				if errType == "FORBIDDEN" || errType == "INSUFFICIENT_SCOPES" || strings.Contains(lowerMsg, "bad credentials") || strings.Contains(lowerMsg, "unauthorized") {
					return nil, false, nil, domain.NewError(
						domain.ErrorCodeExternalUnavailable,
						"sync",
						fmt.Sprintf("GitHub GraphQL authorization failed for %q: %s", repo, trimmedMsg),
						"Ensure GITHUB_TOKEN is valid and has read permissions for repository discussions.",
						nil,
					)
				}
				if trimmedMsg != "" {
					msgs = append(msgs, trimmedMsg)
				}
			}
			joined := strings.Join(msgs, "; ")
			if gqlResp.Data == nil || gqlResp.Data.Repository == nil || gqlResp.Data.Repository.Discussions == nil {
				return nil, false, []string{fmt.Sprintf("Skipped GitHub Discussions sync: %s", joined)}, nil
			}
			if joined != "" {
				warnings = append(warnings, fmt.Sprintf("GitHub Discussions GraphQL warning: %s", joined))
			}
		}

		if gqlResp.Data == nil || gqlResp.Data.Repository == nil || gqlResp.Data.Repository.Discussions == nil {
			return nil, false, []string{fmt.Sprintf("Skipped GitHub Discussions sync: repository %q discussions data unavailable.", repo)}, nil
		}

		conn := gqlResp.Data.Repository.Discussions
		nodes := conn.items()
		if len(nodes) == 0 {
			break
		}

		newOnPage := 0
		reachedSinceBoundary := false

		for _, node := range nodes {
			if node.Number <= 0 {
				continue
			}

			createdAt, err := parseGitHubTime(node.CreatedAt)
			if err != nil {
				return nil, false, nil, domain.NewError(
					domain.ErrorCodeExternalUnavailable,
					"sync",
					fmt.Sprintf("Invalid createdAt timestamp on discussion %s#%d: %v", repo, node.Number, err),
					"Verify GitHub GraphQL timestamp formatting.",
					err,
				)
			}
			updatedAt, err := parseGitHubTime(node.UpdatedAt)
			if err != nil {
				return nil, false, nil, domain.NewError(
					domain.ErrorCodeExternalUnavailable,
					"sync",
					fmt.Sprintf("Invalid updatedAt timestamp on discussion %s#%d: %v", repo, node.Number, err),
					"Verify GitHub GraphQL timestamp formatting.",
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

			if _, exists := seenNumbers[node.Number]; exists {
				continue
			}
			seenNumbers[node.Number] = struct{}{}
			newOnPage++

			if node.Comments != nil && node.Comments.PageInfo.HasNextPage {
				extraNodes, err := c.fetchAdditionalDiscussionComments(ctx, owner, repoName, node.Number, node.Comments.PageInfo.EndCursor)
				if err != nil {
					return nil, false, nil, err
				}
				node.Comments.Nodes = append(node.Comments.items(), extraNodes...)
			}

			cand, err := toDiscussionCandidate(repo, node, createdAt, updatedAt)
			if err != nil {
				return nil, false, nil, err
			}
			candidates = append(candidates, cand)
		}

		if limit > 0 && len(candidates) >= limit {
			break
		}
		if reachedSinceBoundary || newOnPage == 0 {
			break
		}
		if !conn.PageInfo.HasNextPage {
			break
		}
		nextCursor := strings.TrimSpace(conn.PageInfo.EndCursor)
		if nextCursor == "" {
			break
		}
		if _, seen := seenCursors[nextCursor]; seen {
			break
		}
		seenCursors[nextCursor] = struct{}{}
		afterCursor = &nextCursor
	}

	return candidates, true, warnings, nil
}

func (c *Client) fetchAdditionalDiscussionComments(
	ctx context.Context,
	owner, repoName string,
	discussionNumber int,
	startCursor string,
) ([]graphQLCommentNode, error) {
	var (
		extraNodes  []graphQLCommentNode
		seenCursors = make(map[string]struct{})
		cursor      = strings.TrimSpace(startCursor)
	)

	for cursor != "" {
		if err := ctx.Err(); err != nil {
			return nil, domain.AsError("sync", err)
		}
		if _, seen := seenCursors[cursor]; seen {
			break
		}
		seenCursors[cursor] = struct{}{}

		payloadBytes, err := json.Marshal(graphQLRequest{
			Query: discussionCommentsGraphQLQuery,
			Variables: map[string]any{
				"owner":  owner,
				"name":   repoName,
				"number": discussionNumber,
				"first":  defaultPageSize,
				"after":  cursor,
			},
		})
		if err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInternal,
				"sync",
				fmt.Sprintf("Failed to encode discussion comments GraphQL request for #%d: %v", discussionNumber, err),
				"Verify GraphQL request variables.",
				err,
			)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL, bytes.NewReader(payloadBytes))
		if err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeInternal,
				"sync",
				fmt.Sprintf("Failed to construct discussion comments GraphQL request for #%d: %v", discussionNumber, err),
				"Verify the GitHub GraphQL URL configuration.",
				err,
			)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, domain.AsError("sync", ctx.Err())
			}
			return nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("GitHub discussion comments GraphQL request failed for %s/%s#%d: %v", owner, repoName, discussionNumber, err),
				"Check network connectivity and GitHub GraphQL availability.",
				err,
			)
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
		drainAndCloseBody(resp.Body)
		if readErr != nil {
			return nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("Failed to read discussion comments GraphQL response for %s/%s#%d: %v", owner, repoName, discussionNumber, readErr),
				"Retry synchronization or inspect network stability.",
				readErr,
			)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, mapHTTPError(resp.StatusCode, resp.Header, bodyBytes, owner+"/"+repoName, c.token != "")
		}

		var gqlResp graphQLResponse
		if err := json.Unmarshal(bodyBytes, &gqlResp); err != nil {
			return nil, domain.NewError(
				domain.ErrorCodeExternalUnavailable,
				"sync",
				fmt.Sprintf("Invalid discussion comments GraphQL response for %s/%s#%d: %v", owner, repoName, discussionNumber, err),
				"Verify GitHub GraphQL API availability and response format.",
				err,
			)
		}
		if gqlResp.Data == nil || gqlResp.Data.Repository == nil || gqlResp.Data.Repository.Discussion == nil || gqlResp.Data.Repository.Discussion.Comments == nil {
			break
		}
		conn := gqlResp.Data.Repository.Discussion.Comments
		pageItems := conn.items()
		if len(pageItems) == 0 {
			break
		}
		extraNodes = append(extraNodes, pageItems...)
		if !conn.PageInfo.HasNextPage {
			break
		}
		cursor = strings.TrimSpace(conn.PageInfo.EndCursor)
	}

	return extraNodes, nil
}

func toDiscussionCandidate(repo string, node graphQLDiscussionNode, createdAt, updatedAt time.Time) (candidateItem, error) {
	var labels []string
	if node.Labels != nil {
		labels = node.Labels.list()
	} else {
		labels = []string{}
	}

	extID := strings.TrimSpace(node.ID.String())
	if extID == "" {
		extID = fmt.Sprintf("discussion-%d", node.Number)
	}

	discURL := strings.TrimSpace(node.URL)
	if discURL == "" {
		discURL = strings.TrimSpace(node.HTMLURL)
	}
	if discURL == "" {
		discURL = fmt.Sprintf("https://github.com/%s/discussions/%d", repo, node.Number)
	}

	var (
		comments       []domain.Comment
		seenCommentIDs = make(map[string]struct{})
	)

	if node.Comments != nil {
		for cIdx, cNode := range node.Comments.items() {
			cid := strings.TrimSpace(cNode.ID.String())
			if cid == "" {
				cid = fmt.Sprintf("disc-comment-%d-%d", node.Number, cIdx+1)
			}
			if _, exists := seenCommentIDs[cid]; !exists {
				seenCommentIDs[cid] = struct{}{}
				cCreatedAt, err := parseGitHubTime(cNode.CreatedAt)
				if err != nil {
					return candidateItem{}, domain.NewError(
						domain.ErrorCodeExternalUnavailable,
						"sync",
						fmt.Sprintf("Invalid createdAt on discussion comment %q (%s#%d): %v", cid, repo, node.Number, err),
						"Verify GitHub GraphQL timestamp formatting.",
						err,
					)
				}
				cUpdatedAt, err := parseGitHubTime(cNode.UpdatedAt)
				if err != nil {
					return candidateItem{}, domain.NewError(
						domain.ErrorCodeExternalUnavailable,
						"sync",
						fmt.Sprintf("Invalid updatedAt on discussion comment %q (%s#%d): %v", cid, repo, node.Number, err),
						"Verify GitHub GraphQL timestamp formatting.",
						err,
					)
				}
				if cUpdatedAt.IsZero() && !cCreatedAt.IsZero() {
					cUpdatedAt = cCreatedAt
				}
				cURL := strings.TrimSpace(cNode.URL)
				if cURL == "" {
					cURL = strings.TrimSpace(cNode.HTMLURL)
				}
				comments = append(comments, domain.Comment{
					Repository: repo,
					ItemNumber: node.Number,
					CommentID:  cid,
					Author:     cNode.authorLogin(),
					Body:       cNode.bodyText(),
					URL:        cURL,
					CreatedAt:  cCreatedAt,
					UpdatedAt:  cUpdatedAt,
				})
			}

			if cNode.Replies != nil {
				for rIdx, rNode := range cNode.Replies.items() {
					rid := strings.TrimSpace(rNode.ID.String())
					if rid == "" {
						rid = fmt.Sprintf("disc-reply-%d-%d-%d", node.Number, cIdx+1, rIdx+1)
					}
					if _, exists := seenCommentIDs[rid]; exists {
						continue
					}
					seenCommentIDs[rid] = struct{}{}
					rCreatedAt, err := parseGitHubTime(rNode.CreatedAt)
					if err != nil {
						return candidateItem{}, domain.NewError(
							domain.ErrorCodeExternalUnavailable,
							"sync",
							fmt.Sprintf("Invalid createdAt on discussion reply %q (%s#%d): %v", rid, repo, node.Number, err),
							"Verify GitHub GraphQL timestamp formatting.",
							err,
						)
					}
					rUpdatedAt, err := parseGitHubTime(rNode.UpdatedAt)
					if err != nil {
						return candidateItem{}, domain.NewError(
							domain.ErrorCodeExternalUnavailable,
							"sync",
							fmt.Sprintf("Invalid updatedAt on discussion reply %q (%s#%d): %v", rid, repo, node.Number, err),
							"Verify GitHub GraphQL timestamp formatting.",
							err,
						)
					}
					if rUpdatedAt.IsZero() && !rCreatedAt.IsZero() {
						rUpdatedAt = rCreatedAt
					}
					rURL := strings.TrimSpace(rNode.URL)
					if rURL == "" {
						rURL = strings.TrimSpace(rNode.HTMLURL)
					}
					comments = append(comments, domain.Comment{
						Repository: repo,
						ItemNumber: node.Number,
						CommentID:  rid,
						Author:     rNode.authorLogin(),
						Body:       rNode.bodyText(),
						URL:        rURL,
						CreatedAt:  rCreatedAt,
						UpdatedAt:  rUpdatedAt,
					})
				}
			}
		}
	}

	return candidateItem{
		item: domain.Item{
			Repository: repo,
			Number:     node.Number,
			ExternalID: extID,
			Kind:       domain.ItemKindDiscussion,
			State:      node.itemState(),
			Title:      normalizeTitle(node.Title, domain.ItemKindDiscussion, node.Number),
			Body:       node.bodyText(),
			Author:     node.authorLogin(),
			Labels:     labels,
			URL:        discURL,
			CreatedAt:  createdAt,
			UpdatedAt:  updatedAt,
		},
		comments: comments,
	}, nil
}
