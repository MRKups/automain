package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxDecisionRequestBytes is an application budget, not a token/context estimate.
// Keep requests below OpenRouter's roughly 32KB long-context switch. Never truncate.
const MaxDecisionRequestBytes = 30000

type DecisionQuestionType string

const (
	DecisionNoul   DecisionQuestionType = "noul"
	DecisionChoice DecisionQuestionType = "choice"
	DecisionScore  DecisionQuestionType = "score"
)

// Raw JSON preserves structured instructions and criteria without prompt templating.
type DecisionQuestion struct {
	Type         DecisionQuestionType `json:"type"`
	Instructions json.RawMessage      `json:"instructions"`
	Criteria     json.RawMessage      `json:"criteria,omitempty"`
}

// The application selects the model/endpoint, never the incoming request.
type DecisionRequest struct {
	State     json.RawMessage             `json:"state"`
	Questions map[string]DecisionQuestion `json:"questions"`
}

// Pointers distinguish missing/null fields from legitimate zero probabilities.
// Noul has no separate confidence. Choice/Score retain their full distributions.
type DecisionAnswer struct {
	Type          DecisionQuestionType       `json:"type"`
	Noul          *float64                   `json:"noul,omitempty"`
	Choice        string                     `json:"choice,omitempty"`
	Score         *float64                   `json:"score,omitempty"`
	Confidence    *float64                   `json:"confidence,omitempty"`
	Probabilities map[string]*float64        `json:"probabilities,omitempty"`
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`
}

type DecisionUsage struct {
	InputTokens  *int     `json:"input_tokens"`
	OutputTokens *int     `json:"output_tokens"`
	Cost         *float64 `json:"cost,omitempty"`
}

type DecisionResult struct {
	ID       string                    `json:"id,omitempty"`
	Model    string                    `json:"model"`
	Provider string                    `json:"provider,omitempty"`
	Answers  map[string]DecisionAnswer `json:"answers"`
	Usage    DecisionUsage             `json:"usage"`
}

// DecisionProvider makes judgments only. It cannot generate prose or write storage.
type DecisionProvider interface {
	ValidateConfig() error
	Decide(context.Context, DecisionRequest) (DecisionResult, error)
}

func DecodeDecisionRequest(data []byte) (DecisionRequest, error) {
	var request DecisionRequest
	if len(data) > MaxDecisionRequestBytes {
		return request, decisionInputError("Decision request exceeds the 30000-byte budget; nothing was truncated.")
	}
	if err := CheckDecisionJSON(data); err != nil {
		return request, decisionInputError("Decision input must be one UTF-8 JSON value with unique keys, finite numbers, and at most 64 nesting levels.")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, decisionInputError("Decision input must contain only state and typed questions; unknown fields are not accepted.")
	}
	if err := request.Validate(); err != nil {
		return DecisionRequest{}, err
	}
	return request, nil
}

func (r DecisionRequest) Validate() error {
	data, err := json.Marshal(r)
	if err != nil || CheckDecisionJSON(data) != nil {
		return decisionInputError("Decision input contains invalid JSON, duplicate keys, non-finite numbers, or excessive nesting.")
	}
	if len(data) > MaxDecisionRequestBytes {
		return decisionInputError("Decision request exceeds the 30000-byte budget; nothing was truncated.")
	}
	if !decisionContent(r.State, false) || len(r.Questions) == 0 {
		return decisionInputError("Decisions require a string/object/array state and at least one question.")
	}
	for id, q := range r.Questions {
		if strings.TrimSpace(id) == "" || !decisionContent(q.Instructions, false) {
			return decisionInputError("Each question needs a non-empty ID and string/object/array instructions.")
		}
		switch q.Type {
		case DecisionNoul:
			if len(q.Criteria) == 0 {
				continue
			}
			var criteria map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) != 2 ||
				!decisionContent(criteria["true"], false) || !decisionContent(criteria["false"], false) {
				return decisionInputError("Noul criteria must be omitted or describe exactly true and false.")
			}
		case DecisionChoice:
			var criteria map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) < 2 || len(criteria) > 255 {
				return decisionInputError("Choice criteria require a map of 2-255 options.")
			}
			for key, description := range criteria {
				if strings.TrimSpace(key) == "" || !decisionContent(description, true) {
					return decisionInputError("Choice options need non-empty names and string/object/array/null descriptions.")
				}
			}
		case DecisionScore:
			var levels []json.RawMessage
			if json.Unmarshal(q.Criteria, &levels) != nil || len(levels) < 2 || len(levels) > 10 {
				return decisionInputError("Score criteria require an ordered array of 2-10 descriptive levels.")
			}
			for _, level := range levels {
				if !decisionContent(level, false) {
					return decisionInputError("Score levels must be string/object/array descriptions.")
				}
			}
		default:
			return decisionInputError("Supported decision question types are noul, choice, and score; image/box questions are not supported.")
		}
	}
	return nil
}

func decisionContent(raw json.RawMessage, nullable bool) bool {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return false
	}
	if nullable && bytes.Equal(data, []byte("null")) {
		return true
	}
	if data[0] == '{' || data[0] == '[' {
		return true // JSON syntax was checked separately.
	}
	var text string
	return data[0] == '"' && json.Unmarshal(data, &text) == nil && strings.TrimSpace(text) != ""
}

func decisionInputError(message string) error {
	return NewError(ErrorCodeInvalidInput, "decide", message, "Provide state and narrow typed questions; split large contexts explicitly.", nil)
}

// CheckDecisionJSON rejects ambiguous duplicate keys and unsafe numeric/depth
// values that encoding/json alone would silently accept. Errors never echo data.
func CheckDecisionJSON(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8 JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := decisionJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func decisionJSONValue(d *json.Decoder, depth int) error {
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON")
	}
	if n, ok := token.(json.Number); ok {
		value, err := strconv.ParseFloat(string(n), 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return fmt.Errorf("non-finite JSON number")
		}
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	if depth >= 64 || (delim != '{' && delim != '[') {
		return fmt.Errorf("invalid JSON nesting")
	}
	keys := make(map[string]bool)
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || keys[name] {
				return fmt.Errorf("invalid or duplicate JSON key")
			}
			keys[name] = true
		}
		if err := decisionJSONValue(d, depth+1); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
		return fmt.Errorf("invalid JSON container")
	}
	return nil
}
