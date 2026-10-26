package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/MRKups/automain/internal/domain"
)

// Retrieval requests have a flat, typed vocabulary. Reject ambiguous or ignored
// input before invoking operations that can read local files or call providers.
func decodeRetrievalJSONBody(r *http.Request, dst any, fields ...string) error {
	if r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(raw) > maxRequestBodyBytes {
		return fmt.Errorf("request body exceeds maximum size of %d bytes", maxRequestBodyBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("request body must be valid UTF-8 JSON")
	}
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("request body must be one JSON object")
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read JSON field: %w", err)
		}
		field, ok := token.(string)
		if !ok || !allowed[field] {
			return fmt.Errorf("request body contains an unknown field")
		}
		if seen[field] {
			return fmt.Errorf("request body contains a duplicate field")
		}
		seen[field] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("read JSON field value: %w", err)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("request fields must not be null; omit optional fields instead")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("close JSON object: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("request body must contain exactly one JSON object")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode JSON request fields: %w", err)
	}
	return nil
}

func validateRetrievalQuery(r *http.Request, command string, fields ...string) error {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return domain.NewError(domain.ErrorCodeInvalidInput, command, "Retrieval query parameters are malformed.", "Use URL-encoded query parameters for this endpoint.", err)
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return domain.NewError(domain.ErrorCodeInvalidInput, command, "Retrieval query contains an unknown or repeated parameter.", "Use only the documented parameters, with one value per name.", nil)
		}
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(raw) != 0 {
			return domain.NewError(domain.ErrorCodeInvalidInput, command, "GET and HEAD retrieval requests do not accept a request body.", "Use query parameters, or POST a JSON object where supported.", err)
		}
	}
	return nil
}
