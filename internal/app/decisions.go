package app

import (
	"context"

	"github.com/MRKups/automain/internal/domain"
)

// Decide is deliberately policy-free: no retrieval, generated text, thresholds,
// relationship mutations, or legacy analysis. Callers supply the bounded state.
func (s *Service) Decide(ctx context.Context, input domain.DecisionRequest) (domain.DecisionResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.DecisionResult{}, domain.NewError(domain.ErrorCodeExternalUnavailable, "decide", "Decision request was cancelled or timed out.", "Retry explicitly if still needed.", err)
	}
	if err := input.Validate(); err != nil {
		return domain.DecisionResult{}, err
	}
	if s.adapters.Decider == nil {
		return domain.DecisionResult{}, domain.NewError(domain.ErrorCodeNotConfigured, "decide", "Decision provider is not wired.", "Inspect 'automain status' for decision provider availability.", nil)
	}
	return s.adapters.Decider.Decide(ctx, input)
}
