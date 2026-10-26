// Package embedding generates embeddings, independently of relationship analysis.
package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
)

const (
	queryPrefix      = "task: search result | query: "
	inputRecipe      = "gemini-2-retrieval-v1:title-text:search-result-query"
	maxInputBytes    = 8000 // Conservative byte budget, below the 8192-token model limit.
	maxResponseBytes = 32 << 20
)

// OpenRouter uses only google/gemini-embedding-2, without model substitutions or
// automatic paid request retries. Opening the client does not contact the API.
type OpenRouter struct {
	cfg        config.Config
	httpClient *http.Client
}

var _ domain.EmbeddingProvider = (*OpenRouter)(nil)

func NewOpenRouter(cfg config.Config) *OpenRouter {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = config.DefaultTimeout
	}
	return &OpenRouter{cfg: cfg, httpClient: &http.Client{
		Timeout: timeout,
		// Do not replay authenticated embedding requests at redirected endpoints.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *OpenRouter) Space() domain.EmbeddingSpace {
	// An endpoint override must not share cached vectors with the real API.
	sum := sha256.Sum256([]byte(c.cfg.EmbeddingURL() + "\n" + inputRecipe))
	return domain.EmbeddingSpace{
		Provider: config.EmbeddingProvider, Model: config.EmbeddingModel,
		Revision:   "gemini-2-retrieval-v1-" + hex.EncodeToString(sum[:8]),
		Dimensions: c.cfg.EmbeddingSize(),
	}
}

func (c *OpenRouter) ValidateConfig() error {
	if err := c.cfg.ValidateEmbeddingSettings(); err != nil {
		return domain.NewError(domain.ErrorCodeInvalidInput, "embedding", err.Error(), "Check the embedding model, endpoint, and dimensions.", err)
	}
	if !c.cfg.EmbeddingAPIKeySet() {
		return domain.NewError(domain.ErrorCodeNotConfigured, "embedding", "OpenRouter embedding API key is not configured.", "Set OPENROUTER_API_KEY (or AUTOMAIN_EMBEDDING_API_KEY). Gemini/OpenAI analysis credentials are not used for embeddings.", nil)
	}
	return nil
}

func (c *OpenRouter) Embed(ctx context.Context, purpose domain.EmbeddingPurpose, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("embedding", err)
	}
	if err := c.ValidateConfig(); err != nil {
		return nil, err
	}
	if purpose != domain.EmbeddingDocument && purpose != domain.EmbeddingQuery {
		return nil, invalid("Unsupported embedding input purpose.")
	}
	if len(texts) == 0 || len(texts) > 32 {
		return nil, invalid("Embedding requests require between 1 and 32 inputs.")
	}
	inputs := make([]string, len(texts))
	for i, text := range texts {
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
			return nil, invalid("Embedding inputs must be non-empty UTF-8 text.")
		}
		inputs[i] = text
		if purpose == domain.EmbeddingQuery {
			inputs[i] = queryPrefix + text
		}
		if len(inputs[i]) > maxInputBytes {
			return nil, invalid(fmt.Sprintf("Embedding input exceeds the conservative %d-byte limit; split the source or shorten the query. Input was not truncated.", maxInputBytes))
		}
	}
	request := struct {
		Model          string   `json:"model"`
		Input          []string `json:"input"`
		Dimensions     int      `json:"dimensions"`
		EncodingFormat string   `json:"encoding_format"`
	}{config.EmbeddingModel, inputs, c.cfg.EmbeddingSize(), "float"}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, domain.AsError("embedding", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.EmbeddingURL()+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return nil, domain.AsError("embedding", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.EmbeddingAPIKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "automain/0.1.0-dev")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, domain.AsError("embedding", ctx.Err())
		}
		return nil, unavailable("OpenRouter embedding request failed.", "Check connectivity and the embedding endpoint, then retry explicitly.", err)
	}
	defer closeBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not echo untrusted provider errors, which can contain source text,
		// request metadata, or credentials. The status provides a safe diagnosis.
		return nil, statusError(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, unavailable("Could not read the OpenRouter embedding response.", "Retry explicitly after checking network connectivity.", err)
	}
	if len(body) > maxResponseBytes {
		return nil, unavailable("OpenRouter embedding response exceeded the size limit.", "Use smaller embedding batches.", nil)
	}
	var response struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Data   []struct {
			Index     *int       `json:"index"`
			Embedding []*float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, unavailable("OpenRouter returned malformed embedding JSON.", "The embedding response must contain numeric vectors.", nil)
	}
	if response.Object != "list" || response.Model != config.EmbeddingModel || len(response.Data) != len(texts) {
		return nil, unavailable("OpenRouter returned an unexpected model or embedding count.", "The requested model must return exactly one embedding for each input; no fallback model is accepted.", nil)
	}
	vectors := make([][]float32, len(texts))
	for _, entry := range response.Data {
		if entry.Index == nil || *entry.Index < 0 || *entry.Index >= len(texts) || vectors[*entry.Index] != nil {
			return nil, unavailable("OpenRouter returned missing, duplicate, or invalid embedding indices.", "Each response vector must identify its input position exactly once.", nil)
		}
		v := make([]float32, len(entry.Embedding))
		for i, value := range entry.Embedding {
			if value == nil {
				return nil, unavailable("OpenRouter returned a null embedding value.", "Vectors must contain only finite numbers.", nil)
			}
			v[i] = *value
		}
		if err := domain.ValidateVector(v, c.cfg.EmbeddingSize()); err != nil {
			return nil, unavailable("OpenRouter returned an invalid embedding vector.", err.Error(), nil)
		}
		vectors[*entry.Index] = v
	}
	return vectors, nil
}

func statusError(status int) error {
	recovery := "Check OpenRouter availability and retry explicitly."
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		recovery = "Check OPENROUTER_API_KEY and its permissions."
	case http.StatusPaymentRequired:
		recovery = "Check your OpenRouter credit balance and spending limits."
	case http.StatusNotFound:
		recovery = "Check availability of google/gemini-embedding-2 on OpenRouter; no other model will be substituted."
	case http.StatusTooManyRequests:
		recovery = "Wait for OpenRouter's rate limit to reset, then rerun. Saved batches are reused."
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		recovery = "Check the embedding input size and model parameter support."
	}
	return unavailable(fmt.Sprintf("OpenRouter embeddings returned HTTP %d.", status), recovery, nil)
}

func invalid(message string) error {
	return domain.NewError(domain.ErrorCodeInvalidInput, "embedding", message, "Check the embedding inputs and settings.", nil)
}

func unavailable(message, recovery string, err error) error {
	return domain.NewError(domain.ErrorCodeExternalUnavailable, "embedding", message, recovery, err)
}

func closeBody(body io.ReadCloser) {
	if err := body.Close(); err != nil {
		return
	}
}
