package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/MRKups/automain/internal/domain"
)

func boundRequestRead(w http.ResponseWriter, r *http.Request) error {
	deadline, ok := r.Context().Deadline()
	if !ok {
		return nil
	}
	err := http.NewResponseController(w).SetReadDeadline(deadline)
	// In-memory handlers such as ResponseRecorder have no socket deadline.
	// Network middleware must expose the controller methods or Unwrap.
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	if err != nil {
		return domain.NewError(domain.ErrorCodeInternal, "serve",
			"Cannot set the HTTP request read deadline.", "Inspect server connection and middleware errors before retrying.", err)
	}
	return nil
}

func readRequestBody(r *http.Request, limit int64) ([]byte, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("request body exceeds maximum size of %d bytes", limit)
	}
	return raw, nil
}

func transportError(command string, err error) *domain.Error {
	var classified *domain.Error
	if errors.As(err, &classified) && classified.Code == domain.ErrorCodeExternalUnavailable {
		return classified
	}
	var networkError net.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &networkError) && networkError.Timeout()) {
		return domain.NewError(domain.ErrorCodeExternalUnavailable, command,
			"HTTP operation was canceled or exceeded its deadline.",
			"Retry explicitly with a suitable timeout; inspect state before retrying mutations.", err)
	}
	return domain.AsError(command, err)
}
