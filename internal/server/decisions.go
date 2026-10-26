package server

import (
	"context"
	"io"
	"mime"
	"net/http"

	"github.com/MRKups/automain/internal/domain"
)

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if !requireMethod(w, r, "decide", http.MethodPost) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, "decide", domain.NewError(domain.ErrorCodeInvalidInput, "decide", "Decision requests require Content-Type: application/json.", "Send a JSON body containing state and questions.", nil))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxDecisionRequestBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, "decide", domain.NewError(domain.ErrorCodeInvalidInput, "decide", "Cannot read decision body or body exceeds the 30000-byte budget.", "Send bounded JSON; input is never truncated.", nil))
		return
	}
	input, err := domain.DecodeDecisionRequest(data)
	if err != nil {
		writeAPIError(w, "decide", err)
		return
	}
	result, err := s.svc.Decide(ctx, input)
	if err != nil {
		writeAPIError(w, "decide", err)
		return
	}
	writeAPISuccess(w, http.StatusOK, "decide", result)
}
