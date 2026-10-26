package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"google.golang.org/genai"
)

const (
	DefaultBaseURL       = config.DefaultOpenRouterBaseURL
	DefaultModel         = config.DefaultLLMModel
	DefaultUserAgent     = "automain/0.1.0-dev"
	defaultHTTPTimeout   = 60 * time.Second
	maxCandidatesDefault = 10
)

// ClientConfig configures the LLM client used for candidate discovery and relationship analysis.
type ClientConfig struct {
	Provider         string
	APIKey           string
	Model            string
	BaseURL          string
	APIVersion       string
	HTTPClient       *http.Client
	Timeout          time.Duration
	UserAgent        string
	AllowEmptyAPIKey bool
}

// Client implements domain.RelationshipAnalyser and backs the LLM candidate finder
// using the official Google GenAI SDK (google.golang.org/genai) and OpenAI Go SDK (github.com/openai/openai-go).
type Client struct {
	provider         string
	apiKey           string
	model            string
	baseURL          string
	apiVersion       string
	httpClient       *http.Client
	userAgent        string
	allowEmptyAPIKey bool
}

// NewClient constructs an LLM client from runtime configuration and environment overrides.
func NewClient(cfg config.Config) *Client {
	provider := config.NormalizeLLMProvider(cfg.LLMProvider)
	baseURL := strings.TrimSpace(cfg.LLMBaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_API_URL"))
	}
	if baseURL == "" {
		switch provider {
		case "openrouter":
			baseURL = strings.TrimSpace(os.Getenv("OPENROUTER_API_URL"))
			if baseURL == "" {
				baseURL = strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
			}
		case "openai":
			baseURL = strings.TrimSpace(os.Getenv("OPENAI_API_URL"))
			if baseURL == "" {
				baseURL = strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
			}
		case "deepseek":
			baseURL = strings.TrimSpace(os.Getenv("DEEPSEEK_API_URL"))
			if baseURL == "" {
				baseURL = strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
			}
		default:
			baseURL = strings.TrimSpace(os.Getenv("GEMINI_API_URL"))
		}
	}
	apiVersion := strings.TrimSpace(os.Getenv("AUTOMAIN_GEMINI_API_VERSION"))
	return NewClientWithConfig(ClientConfig{
		Provider:   provider,
		APIKey:     cfg.LLMAPIKey,
		Model:      cfg.LLMModel,
		BaseURL:    baseURL,
		APIVersion: apiVersion,
		Timeout:    cfg.Timeout,
	})
}

// NewClientWithConfig constructs an LLM client from explicit ClientConfig settings.
func NewClientWithConfig(cfg ClientConfig) *Client {
	provider := config.NormalizeLLMProvider(cfg.Provider)
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		switch provider {
		case "gemini":
			baseURL = config.DefaultGeminiBaseURL
		case "openai":
			baseURL = config.DefaultOpenAIBaseURL
		case "deepseek":
			baseURL = config.DefaultDeepSeekBaseURL
		default:
			baseURL = DefaultBaseURL
		}
	}

	rawModel := strings.TrimSpace(cfg.Model)
	if provider == "gemini" {
		rawModel = strings.TrimSpace(strings.TrimPrefix(rawModel, "models/"))
	}
	model := rawModel
	if model == "" {
		switch provider {
		case "gemini":
			model = config.DefaultGeminiModel
		case "openai":
			model = config.DefaultOpenAIModel
		case "deepseek":
			model = config.DefaultDeepSeekModel
		default:
			model = DefaultModel
		}
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
		provider:         provider,
		apiKey:           strings.TrimSpace(cfg.APIKey),
		model:            model,
		baseURL:          baseURL,
		apiVersion:       strings.TrimSpace(cfg.APIVersion),
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
			fmt.Sprintf("LLM API key is not configured for provider %q.", c.provider),
			"Set AUTOMAIN_LLM_API_KEY or the selected provider's key: OPENROUTER_API_KEY, GEMINI_API_KEY, OPENAI_API_KEY, or DEEPSEEK_API_KEY.",
			nil,
		)
	}
	return nil
}

func (c *Client) isOpenAICompatible() bool {
	if c.provider == "openrouter" || c.provider == "openai" || c.provider == "deepseek" {
		return true
	}
	lowerURL := strings.ToLower(c.baseURL)
	return strings.HasSuffix(lowerURL, "/chat/completions") ||
		strings.Contains(lowerURL, "api.openai.com") ||
		strings.Contains(lowerURL, "api.deepseek.com")
}

func (c *Client) generateJSON(ctx context.Context, systemPrompt, userPrompt string, schema map[string]any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("analyze", err)
	}
	if err := c.ValidateConfig(); err != nil {
		return nil, err
	}

	if c.isOpenAICompatible() {
		return c.generateWithOpenAISDK(ctx, systemPrompt, userPrompt, schema)
	}
	return c.generateWithGeminiSDK(ctx, systemPrompt, userPrompt, schema)
}

func (c *Client) generateWithGeminiSDK(ctx context.Context, systemPrompt, userPrompt string, schema map[string]any) ([]byte, error) {
	baseHost, apiVer := splitGeminiBaseURLAndVersion(c.baseURL, c.apiVersion)
	headers := http.Header{}
	headers.Set("User-Agent", c.userAgent)

	apiKey := c.apiKey
	if apiKey == "" && c.allowEmptyAPIKey {
		apiKey = "test-empty-key"
	}

	genaiClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: c.httpClient,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    baseHost,
			APIVersion: apiVer,
			Headers:    headers,
		},
	})
	if err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInternal,
			"analyze",
			fmt.Sprintf("Failed to initialize Google GenAI SDK client: %v", err),
			"Verify AUTOMAIN_LLM_API_KEY and AUTOMAIN_LLM_API_URL.",
			err,
		)
	}

	temp := float32(0.1)
	genCfg := &genai.GenerateContentConfig{
		Temperature:        &temp,
		ResponseMIMEType:   "application/json",
		ResponseJsonSchema: normalizeJSONSchemaForGenAI(schema),
	}
	if strings.TrimSpace(systemPrompt) != "" {
		genCfg.SystemInstruction = genai.NewContentFromText(systemPrompt, genai.RoleUser)
	}

	resp, err := genaiClient.Models.GenerateContent(ctx, c.model, genai.Text(userPrompt), genCfg)
	if err != nil {
		if ctx.Err() != nil {
			return nil, domain.AsError("analyze", ctx.Err())
		}
		var apiErr genai.APIError
		if errors.As(err, &apiErr) {
			return nil, mapLLMStatusAndDetail(apiErr.Code, apiErr.Message)
		}
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("LLM API request failed: %v", err),
			"Check network connectivity and LLM service availability, or increase --timeout.",
			err,
		)
	}

	if resp == nil || len(resp.Candidates) == 0 {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			"Failed to extract structured JSON from LLM response: LLM response contained no candidates",
			"Verify that the configured --finder and AUTOMAIN_LLM_MODEL support structured JSON output.",
			nil,
		)
	}

	text := strings.TrimSpace(resp.Text())
	if text == "" {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			"Failed to extract structured JSON from LLM response: LLM response contained no candidate content parts",
			"Verify that the configured --finder and AUTOMAIN_LLM_MODEL support structured JSON output.",
			nil,
		)
	}

	extracted, cleanErr := cleanMarkdownJSON(text)
	if cleanErr != nil {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("Failed to extract structured JSON from LLM response: %v", cleanErr),
			"Verify that the configured --finder and AUTOMAIN_LLM_MODEL support structured JSON output.",
			cleanErr,
		)
	}
	return extracted, nil
}

func (c *Client) generateWithOpenAISDK(ctx context.Context, systemPrompt, userPrompt string, schema map[string]any) ([]byte, error) {
	baseURL := normalizeOpenAIBaseURL(c.baseURL)
	apiKey := c.apiKey
	if apiKey == "" && c.allowEmptyAPIKey {
		apiKey = "test-empty-key"
	}

	oaiClient := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(c.httpClient),
		option.WithHeader("User-Agent", c.userAgent),
		option.WithMaxRetries(0),
	)

	sys := strings.TrimSpace(systemPrompt)
	if len(schema) > 0 {
		if schemaBytes, err := json.Marshal(normalizeJSONSchemaForGenAI(schema)); err == nil {
			if sys != "" {
				sys += "\n\n"
			}
			sys += "Respond ONLY with a valid JSON object conforming to this JSON schema: " + string(schemaBytes)
		}
	}

	var msgs []openai.ChatCompletionMessageParamUnion
	if sys != "" {
		msgs = append(msgs, openai.SystemMessage(sys))
	}
	msgs = append(msgs, openai.UserMessage(userPrompt))

	isReasoner := strings.Contains(strings.ToLower(c.model), "reasoner")

	buildParams := func(useJSONObject bool) openai.ChatCompletionNewParams {
		p := openai.ChatCompletionNewParams{
			Model:    shared.ChatModel(c.model),
			Messages: msgs,
		}
		if !isReasoner {
			p.Temperature = openai.Float(0.1)
			if useJSONObject {
				jsonFmt := shared.NewResponseFormatJSONObjectParam()
				p.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
					OfJSONObject: &jsonFmt,
				}
			}
		}
		return p
	}

	res, err := oaiClient.Chat.Completions.New(ctx, buildParams(true))
	if err != nil {
		var oaiErr *openai.Error
		if errors.As(err, &oaiErr) && oaiErr.StatusCode == http.StatusBadRequest && !isReasoner {
			// Retry once without response_format if the model endpoint does not support json_object mode.
			if retryRes, retryErr := oaiClient.Chat.Completions.New(ctx, buildParams(false)); retryErr == nil {
				res = retryRes
				err = nil
			}
		}
	}

	if err != nil {
		if ctx.Err() != nil {
			return nil, domain.AsError("analyze", ctx.Err())
		}
		var oaiErr *openai.Error
		if errors.As(err, &oaiErr) {
			return nil, mapLLMStatusAndDetail(oaiErr.StatusCode, oaiErr.Message)
		}
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("LLM API request failed: %v", err),
			"Check network connectivity and LLM service availability, or increase --timeout.",
			err,
		)
	}

	if res == nil || len(res.Choices) == 0 {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			"Failed to extract structured JSON from LLM response: LLM response contained no choices",
			"Verify that the configured --finder and AUTOMAIN_LLM_MODEL support structured JSON output.",
			nil,
		)
	}

	text := strings.TrimSpace(res.Choices[0].Message.Content)
	if text == "" {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			"Failed to extract structured JSON from LLM response: LLM response contained empty choice content",
			"Verify that the configured --finder and AUTOMAIN_LLM_MODEL support structured JSON output.",
			nil,
		)
	}

	extracted, cleanErr := cleanMarkdownJSON(text)
	if cleanErr != nil {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("Failed to extract structured JSON from LLM response: %v", cleanErr),
			"Verify that the configured --finder and AUTOMAIN_LLM_MODEL support structured JSON output.",
			cleanErr,
		)
	}
	return extracted, nil
}

func splitGeminiBaseURLAndVersion(rawURL, explicitVersion string) (string, string) {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	apiVer := strings.TrimSpace(explicitVersion)
	if strings.HasSuffix(trimmed, "/v1beta") {
		trimmed = strings.TrimSuffix(trimmed, "/v1beta")
		if apiVer == "" {
			apiVer = "v1beta"
		}
	} else if strings.HasSuffix(trimmed, "/v1") {
		trimmed = strings.TrimSuffix(trimmed, "/v1")
		if apiVer == "" {
			apiVer = "v1"
		}
	}
	if apiVer == "" {
		apiVer = "v1beta"
	}
	if trimmed == "" {
		trimmed = "https://generativelanguage.googleapis.com"
	}
	return trimmed + "/", apiVer
}

func normalizeOpenAIBaseURL(rawURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	trimmed = strings.TrimSuffix(trimmed, "/chat/completions")
	if strings.EqualFold(trimmed, "https://api.openai.com") {
		trimmed = "https://api.openai.com/v1"
	}
	if trimmed == "" {
		trimmed = config.DefaultOpenAIBaseURL
	}
	return trimmed + "/"
}

func normalizeJSONSchemaForGenAI(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		if k == "type" {
			if s, ok := v.(string); ok {
				out[k] = strings.ToLower(s)
				continue
			}
		}
		if subMap, ok := v.(map[string]any); ok {
			out[k] = normalizeJSONSchemaForGenAI(subMap)
			continue
		}
		out[k] = v
	}
	return out
}

func mapLLMStatusAndDetail(statusCode int, detail string) error {
	detail = strings.TrimSpace(detail)
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
			"Check AUTOMAIN_LLM_API_KEY or the selected provider's key and permissions.",
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
			"Verify AUTOMAIN_LLM_PROVIDER and AUTOMAIN_LLM_MODEL, then retry analysis.",
			nil,
		)
	}
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
	if json.Valid([]byte(bytes.TrimSpace([]byte(s)))) {
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
