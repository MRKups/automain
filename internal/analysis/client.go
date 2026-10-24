package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
)

const (
	DefaultBaseURL       = "https://generativelanguage.googleapis.com/v1beta"
	DefaultModel         = config.DefaultLLMModel
	DefaultUserAgent     = "automain/0.1.0-dev"
	defaultHTTPTimeout   = 60 * time.Second
	maxResponseBodyBytes = 32 << 20
	maxCandidatesDefault = 10
)

// ClientConfig configures the Gemini LLM client used for candidate discovery and relationship analysis.
type ClientConfig struct {
	APIKey           string
	Model            string
	BaseURL          string
	HTTPClient       *http.Client
	Timeout          time.Duration
	UserAgent        string
	AllowEmptyAPIKey bool
}

// Client implements domain.RelationshipAnalyser and backs the LLM candidate finder.
type Client struct {
	apiKey           string
	model            string
	baseURL          string
	httpClient       *http.Client
	userAgent        string
	allowEmptyAPIKey bool
}

// NewClient constructs a Gemini LLM client from runtime configuration and environment overrides.
// Optional environment overrides AUTOMAIN_LLM_API_URL and GEMINI_API_URL are respected when present.
func NewClient(cfg config.Config) *Client {
	baseURL := strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_API_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("GEMINI_API_URL"))
	}
	return NewClientWithConfig(ClientConfig{
		APIKey:  cfg.LLMAPIKey,
		Model:   cfg.LLMModel,
		BaseURL: baseURL,
		Timeout: cfg.Timeout,
	})
}

// NewClientWithConfig constructs a Gemini LLM client from explicit ClientConfig settings.
func NewClientWithConfig(cfg ClientConfig) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	model := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cfg.Model), "models/"))
	if model == "" {
		model = DefaultModel
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: timeout,
		}
	}

	userAgent := strings.TrimSpace(cfg.UserAgent)
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	return &Client{
		apiKey:           strings.TrimSpace(cfg.APIKey),
		model:            model,
		baseURL:          baseURL,
		httpClient:       httpClient,
		userAgent:        userAgent,
		allowEmptyAPIKey: cfg.AllowEmptyAPIKey,
	}
}

// ValidateConfig verifies that the LLM client has valid credentials configured.
func (c *Client) ValidateConfig() error {
	if c == nil {
		return domain.NewError(
			domain.ErrorCodeNotConfigured,
			"analyze",
			"Relationship analyser client is not initialized.",
			"Run 'automain status' to inspect adapter availability.",
			nil,
		)
	}
	if c.apiKey == "" && !c.allowEmptyAPIKey {
		return domain.NewError(
			domain.ErrorCodeNotConfigured,
			"analyze",
			"LLM API key is not configured (AUTOMAIN_LLM_API_KEY or GEMINI_API_KEY).",
			"Set AUTOMAIN_LLM_API_KEY or GEMINI_API_KEY in the environment before running 'automain analyze'.",
			nil,
		)
	}
	return nil
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature      float64        `json:"temperature"`
	ResponseMimeType string         `json:"responseMimeType,omitempty"`
	ResponseSchema   map[string]any `json:"responseSchema,omitempty"`
}

type geminiGenerateRequest struct {
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	Contents          []geminiContent        `json:"contents"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiGenerateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (c *Client) endpointURL() string {
	if strings.HasSuffix(c.baseURL, ":generateContent") || strings.HasSuffix(c.baseURL, "/chat/completions") {
		return c.baseURL
	}
	if strings.Contains(c.baseURL, "/models/") {
		return c.baseURL + ":generateContent"
	}
	return fmt.Sprintf("%s/models/%s:generateContent", c.baseURL, url.PathEscape(c.model))
}

func (c *Client) generateJSON(ctx context.Context, systemPrompt, userPrompt string, schema map[string]any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("analyze", err)
	}
	if err := c.ValidateConfig(); err != nil {
		return nil, err
	}

	reqPayload := geminiGenerateRequest{
		Contents: []geminiContent{
			{
				Role:  "user",
				Parts: []geminiPart{{Text: userPrompt}},
			},
		},
		GenerationConfig: geminiGenerationConfig{
			Temperature:      0.1,
			ResponseMimeType: "application/json",
			ResponseSchema:   schema,
		},
	}
	if strings.TrimSpace(systemPrompt) != "" {
		reqPayload.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		}
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInternal,
			"analyze",
			fmt.Sprintf("Failed to encode LLM request payload: %v", err),
			"Verify input item and candidate thread data.",
			err,
		)
	}

	reqURL := c.endpointURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInternal,
			"analyze",
			fmt.Sprintf("Failed to build LLM HTTP request: %v", err),
			"Verify the LLM endpoint URL configuration.",
			err,
		)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("x-goog-api-key", c.apiKey)
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, domain.AsError("analyze", ctx.Err())
		}
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("LLM API request failed: %v", err),
			"Check network connectivity and LLM service availability, or increase --timeout.",
			err,
		)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if readErr != nil {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("Failed to read LLM API response body: %v", readErr),
			"Retry analysis or check network stability.",
			readErr,
		)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, mapLLMHTTPError(resp.StatusCode, respBytes)
	}

	extracted, err := extractJSONFromResponse(respBytes)
	if err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("Failed to extract structured JSON from LLM response: %v", err),
			"Verify that the configured --finder and AUTOMAIN_LLM_MODEL support structured JSON output.",
			err,
		)
	}
	return extracted, nil
}

func mapLLMHTTPError(statusCode int, body []byte) error {
	var env geminiGenerateResponse
	_ = json.Unmarshal(body, &env)
	detail := ""
	if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
		detail = strings.TrimSpace(env.Error.Message)
	} else {
		var genericErr struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(body, &genericErr) == nil {
			if strings.TrimSpace(genericErr.Message) != "" {
				detail = strings.TrimSpace(genericErr.Message)
			} else if strings.TrimSpace(genericErr.Error) != "" {
				detail = strings.TrimSpace(genericErr.Error)
			}
		}
	}

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		msg := fmt.Sprintf("LLM API authentication failed (HTTP %d).", statusCode)
		if detail != "" {
			msg = fmt.Sprintf("LLM API authentication failed (HTTP %d: %s).", statusCode, detail)
		}
		return domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			msg,
			"Verify that AUTOMAIN_LLM_API_KEY or GEMINI_API_KEY is valid and has Gemini API access.",
			nil,
		)
	case http.StatusTooManyRequests:
		msg := "LLM API rate limit exceeded (HTTP 429)."
		if detail != "" {
			msg = fmt.Sprintf("LLM API rate limit exceeded (HTTP 429: %s).", detail)
		}
		return domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			msg,
			"Wait for the LLM rate limit window to reset or pass a smaller --limit.",
			nil,
		)
	default:
		msg := fmt.Sprintf("LLM API returned HTTP %d.", statusCode)
		if detail != "" {
			msg = fmt.Sprintf("LLM API returned HTTP %d: %s.", statusCode, detail)
		}
		return domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			msg,
			"Verify AUTOMAIN_LLM_MODEL and retry analysis later.",
			nil,
		)
	}
}

func extractJSONFromResponse(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty response body")
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawMap); err == nil {
		if rawCandidates, hasCandidates := rawMap["candidates"]; hasCandidates {
			var stringCandidates []string
			if json.Unmarshal(rawCandidates, &stringCandidates) == nil {
				return cleanMarkdownJSON(string(trimmed))
			}
		}

		var env geminiGenerateResponse
		if err := json.Unmarshal(trimmed, &env); err == nil {
			if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
				return nil, fmt.Errorf("API error: %s", strings.TrimSpace(env.Error.Message))
			}
		}

		_, hasCandidates := rawMap["candidates"]
		_, hasPromptFeedback := rawMap["promptFeedback"]
		if hasCandidates || hasPromptFeedback {
			if len(env.Candidates) == 0 {
				return nil, fmt.Errorf("LLM response contained no candidates")
			}
			var combined strings.Builder
			for _, part := range env.Candidates[0].Content.Parts {
				if strings.TrimSpace(part.Text) != "" {
					combined.WriteString(part.Text)
				}
			}
			text := strings.TrimSpace(combined.String())
			if text == "" {
				return nil, fmt.Errorf("LLM response contained no candidate content parts")
			}
			return cleanMarkdownJSON(text)
		}

		if _, hasChoices := rawMap["choices"]; hasChoices {
			if len(env.Choices) == 0 {
				return nil, fmt.Errorf("LLM response contained no choices")
			}
			text := strings.TrimSpace(env.Choices[0].Message.Content)
			if text == "" {
				return nil, fmt.Errorf("LLM response contained empty choice content")
			}
			return cleanMarkdownJSON(text)
		}
	}

	return cleanMarkdownJSON(string(trimmed))
}

func cleanMarkdownJSON(text string) ([]byte, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		if firstNewline := strings.IndexByte(s, '\n'); firstNewline >= 0 {
			s = s[firstNewline+1:]
		} else {
			s = strings.TrimPrefix(s, "```json")
			s = strings.TrimPrefix(s, "```")
		}
		if lastFence := strings.LastIndex(s, "```"); lastFence >= 0 {
			s = s[:lastFence]
		}
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return nil, fmt.Errorf("empty JSON content")
	}
	if json.Valid([]byte(s)) {
		return []byte(s), nil
	}

	startObj := strings.IndexByte(s, '{')
	endObj := strings.LastIndexByte(s, '}')
	startArr := strings.IndexByte(s, '[')
	endArr := strings.LastIndexByte(s, ']')

	tryObj := func() ([]byte, bool) {
		if startObj >= 0 && endObj > startObj {
			candidate := s[startObj : endObj+1]
			if json.Valid([]byte(candidate)) {
				return []byte(candidate), true
			}
		}
		return nil, false
	}
	tryArr := func() ([]byte, bool) {
		if startArr >= 0 && endArr > startArr {
			candidate := s[startArr : endArr+1]
			if json.Valid([]byte(candidate)) {
				return []byte(candidate), true
			}
		}
		return nil, false
	}

	if startArr >= 0 && (startObj < 0 || startArr < startObj) {
		if out, ok := tryArr(); ok {
			return out, nil
		}
		if out, ok := tryObj(); ok {
			return out, nil
		}
	} else {
		if out, ok := tryObj(); ok {
			return out, nil
		}
		if out, ok := tryArr(); ok {
			return out, nil
		}
	}

	return nil, fmt.Errorf("invalid JSON output from model")
}
