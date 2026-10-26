// Package decision calls Jev's typed Decisions API, not chat completions.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
)

const maxResponseBytes = 1 << 20

type OpenRouter struct {
	cfg        config.Config
	httpClient *http.Client
}

var _ domain.DecisionProvider = (*OpenRouter)(nil)

// Construction/status never makes a request. No retries, redirects, or LLM fallback.
func NewOpenRouter(cfg config.Config) *OpenRouter {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = config.DefaultTimeout
	}
	return &OpenRouter{cfg: cfg, httpClient: &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *OpenRouter) ValidateConfig() error {
	if err := c.cfg.ValidateDecisionSettings(); err != nil {
		return domain.NewError(domain.ErrorCodeInvalidInput, "decide", err.Error(), "Check AUTOMAIN_DECISION_MODEL and AUTOMAIN_DECISION_ENDPOINT.", err)
	}
	if !c.cfg.DecisionAPIKeySet() {
		return domain.NewError(domain.ErrorCodeNotConfigured, "decide", "OpenRouter decision API key is not configured.", "Set OPENROUTER_API_KEY or AUTOMAIN_DECISION_API_KEY. Embedding-specific and legacy LLM keys are not used.", nil)
	}
	return nil
}

func (c *OpenRouter) Decide(ctx context.Context, input domain.DecisionRequest) (domain.DecisionResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.DecisionResult{}, requestFailure(ctx, err)
	}
	if err := input.Validate(); err != nil {
		return domain.DecisionResult{}, err
	}
	if err := c.ValidateConfig(); err != nil {
		return domain.DecisionResult{}, err
	}
	request := struct {
		Model string `json:"model"`
		domain.DecisionRequest
	}{c.cfg.DecisionModelName(), input}
	data, err := json.Marshal(request)
	if err != nil {
		return domain.DecisionResult{}, domain.NewError(domain.ErrorCodeInvalidInput, "decide", "Cannot encode decision input.", "Provide valid JSON state and questions.", nil)
	}
	if len(data) > domain.MaxDecisionRequestBytes {
		return domain.DecisionResult{}, domain.NewError(domain.ErrorCodeInvalidInput, "decide", "Decision request including model exceeds the 30000-byte budget; nothing was truncated.", "Split the state/questions into explicitly bounded requests.", nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.DecisionURL(), bytes.NewReader(data))
	if err != nil {
		return domain.DecisionResult{}, unavailable("Cannot construct the Jev decision request.", "Check the configured decision endpoint.", nil)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.DecisionAPIKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "automain/0.1.0-dev")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return domain.DecisionResult{}, requestFailure(ctx, err)
	}
	defer closeBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return domain.DecisionResult{}, statusError(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return domain.DecisionResult{}, requestFailure(ctx, err)
	}
	if len(body) > maxResponseBytes {
		return domain.DecisionResult{}, unavailable("Jev response exceeded the 1 MiB limit.", "Ask fewer questions per request.", nil)
	}
	if domain.CheckDecisionJSON(body) != nil {
		return domain.DecisionResult{}, badResponse()
	}
	var response struct {
		domain.DecisionResult
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || (len(response.Error) != 0 && string(response.Error) != "null") {
		return domain.DecisionResult{}, badResponse()
	}
	if err := validateResult(input, response.DecisionResult, c.cfg.DecisionModelName()); err != nil {
		return domain.DecisionResult{}, err
	}
	return response.DecisionResult, nil
}

func requestFailure(ctx context.Context, cause error) error {
	message := "OpenRouter Jev decision request failed."
	if ctx.Err() != nil {
		message = "OpenRouter Jev decision request was cancelled or timed out."
	}
	return unavailable(message, "Check connectivity, endpoint, and timeout; retry explicitly. No automatic retry was made.", cause)
}

func statusError(status int) error {
	recovery := "Check OpenRouter availability, then retry explicitly."
	switch status {
	case 401, 403:
		recovery = "Check OPENROUTER_API_KEY or AUTOMAIN_DECISION_API_KEY and permissions."
	case 402:
		recovery = "Check your OpenRouter balance and spending limits."
	case 404:
		recovery = "Check the full Decisions endpoint and configured Jev model; no fallback model is used."
	case 400, 413, 422:
		recovery = "Check decision question shapes and input size; reduce context explicitly."
	case 429, 529:
		recovery = "Wait before retrying explicitly; no automatic paid retries were made."
	}
	return unavailable(fmt.Sprintf("OpenRouter decisions returned HTTP %d.", status), recovery, nil)
}

func badResponse() error {
	return unavailable("OpenRouter returned an invalid Jev decision response.", "Expected the requested Jev model, one correctly typed answer per question, valid probabilities, and usage. No partial decisions were returned.", nil)
}

func unavailable(message, recovery string, cause error) error {
	return domain.NewError(domain.ErrorCodeExternalUnavailable, "decide", message, recovery, cause)
}

func closeBody(body io.ReadCloser) {
	if err := body.Close(); err != nil {
		return
	}
}
