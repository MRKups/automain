package decision

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/MRKups/automain/internal/domain"
)

func validateResult(input domain.DecisionRequest, result domain.DecisionResult, requestedModel string) error {
	// OpenRouter may return the dated revision of a pinned release. Preserve it.
	modelOK := result.Model == requestedModel || strings.HasPrefix(result.Model, requestedModel+"-")
	if requestedModel == "~typesafe/jev-latest" {
		modelOK = strings.HasPrefix(result.Model, "typesafe/jev-") && len(result.Model) > len("typesafe/jev-")
	}
	u := result.Usage
	if !modelOK || len(result.Answers) != len(input.Questions) ||
		u.InputTokens == nil || u.OutputTokens == nil || *u.InputTokens < 0 || *u.OutputTokens < 0 ||
		(u.Cost != nil && (!finite(*u.Cost) || *u.Cost < 0)) {
		return badResponse()
	}
	for id, question := range input.Questions {
		answer, ok := result.Answers[id]
		if !ok || answer.Type != question.Type {
			return badResponse()
		}
		if question.Type == domain.DecisionNoul {
			if !probability(answer.Noul) || answer.Choice != "" || answer.Score != nil || answer.Confidence != nil || len(answer.Probabilities) != 0 || len(answer.Legend) != 0 {
				return badResponse()
			}
			continue
		}
		if answer.Noul != nil || !probability(answer.Confidence) {
			return badResponse()
		}
		var criteria map[string]json.RawMessage
		if question.Type == domain.DecisionChoice {
			if json.Unmarshal(question.Criteria, &criteria) != nil || answer.Score != nil || len(answer.Legend) != 0 {
				return badResponse()
			}
		} else {
			var levels []json.RawMessage
			if json.Unmarshal(question.Criteria, &levels) != nil || answer.Choice != "" || answer.Score == nil || !finite(*answer.Score) {
				return badResponse()
			}
			criteria = make(map[string]json.RawMessage, len(levels))
			for i, level := range levels {
				criteria[strconv.Itoa(i)] = level
			}
			if len(answer.Legend) != len(criteria) || *answer.Score < 0 || *answer.Score > float64(len(criteria)-1) {
				return badResponse()
			}
			for key, expected := range criteria {
				if !sameJSON(expected, answer.Legend[key]) {
					return badResponse()
				}
			}
		}
		if len(answer.Probabilities) != len(criteria) {
			return badResponse()
		}
		total, maximum, weightedScore := 0.0, 0.0, 0.0
		for key := range criteria {
			p := answer.Probabilities[key]
			if !probability(p) {
				return badResponse()
			}
			total += *p
			maximum = math.Max(maximum, *p)
			if question.Type == domain.DecisionScore {
				i, err := strconv.Atoi(key)
				if err != nil {
					return badResponse()
				}
				weightedScore += float64(i) * *p
			}
		}
		// Allow small provider rounding, never normalize or repair bad output.
		if math.Abs(total-1) > 0.01 {
			return badResponse()
		}
		if question.Type == domain.DecisionChoice {
			selected := answer.Probabilities[answer.Choice]
			if selected == nil || *selected < maximum-0.000001 {
				return badResponse()
			}
		} else if math.Abs(*answer.Score-weightedScore) > 0.02 {
			return badResponse()
		}
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func probability(value *float64) bool {
	return value != nil && finite(*value) && *value >= 0 && *value <= 1
}

func sameJSON(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	canonicalLeft, err := json.Marshal(left)
	if err != nil {
		return false
	}
	canonicalRight, err := json.Marshal(right)
	return err == nil && bytes.Equal(canonicalLeft, canonicalRight)
}
