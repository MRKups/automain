package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/MRKups/automain/internal/domain"
)

const (
	StrategyLLM    = "llm"
	StrategyFTS    = "fts"
	StrategyVector = "vector"
)

// NewFinder constructs a domain.CandidateFinder for the requested strategy ("llm", "fts", or "vector").
func NewFinder(strategy string, client *Client) (domain.CandidateFinder, error) {
	norm := strings.ToLower(strings.TrimSpace(strategy))
	if norm == "" {
		norm = StrategyLLM
	}
	switch norm {
	case StrategyFTS:
		return &FTSFinder{MaxCandidates: maxCandidatesDefault}, nil
	case StrategyVector:
		return &VectorFinder{MaxCandidates: maxCandidatesDefault}, nil
	case StrategyLLM:
		return &LLMFinder{
			client:        client,
			MaxCandidates: maxCandidatesDefault,
		}, nil
	default:
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			fmt.Sprintf("Unsupported candidate finder strategy %q.", strategy),
			"Choose one of: --finder llm, --finder fts, or --finder vector.",
			nil,
		)
	}
}

type scoredThread struct {
	summary domain.ThreadSummary
	score   float64
}

func quantizeScore(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return int64(math.Round(v * 1e9))
}

func sortScoredThreads(scored []scoredThread) {
	sort.Slice(scored, func(i, j int) bool {
		qi := quantizeScore(scored[i].score)
		qj := quantizeScore(scored[j].score)
		if qi != qj {
			return qi > qj
		}
		ai := scored[i].summary.Status == domain.ThreadStatusActive
		aj := scored[j].summary.Status == domain.ThreadStatusActive
		if ai != aj {
			return ai
		}
		if scored[i].summary.ItemCount != scored[j].summary.ItemCount {
			return scored[i].summary.ItemCount > scored[j].summary.ItemCount
		}
		return scored[i].summary.ID < scored[j].summary.ID
	})
}

func deduplicateThreads(threads []domain.ThreadSummary) ([]domain.ThreadSummary, map[domain.ThreadID]domain.ThreadSummary) {
	valid := make([]domain.ThreadSummary, 0, len(threads))
	byID := make(map[domain.ThreadID]domain.ThreadSummary, len(threads))
	for _, t := range threads {
		id := domain.ThreadID(strings.TrimSpace(string(t.ID)))
		if id == "" {
			continue
		}
		if _, exists := byID[id]; exists {
			continue
		}
		t.ID = id
		valid = append(valid, t)
		byID[id] = t
	}
	return valid, byID
}

// FTSFinder narrows candidate threads using BM25 lexical scoring over tokenized and stemmed terms.
type FTSFinder struct {
	MaxCandidates int
}

// FindCandidates scores threads against item using BM25 over title, labels, and body tokens.
func (f *FTSFinder) FindCandidates(ctx context.Context, repo string, item domain.Item, threads []domain.ThreadSummary) ([]domain.ThreadID, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("analyze", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}

	validThreads, _ := deduplicateThreads(threads)
	if len(validThreads) == 0 {
		return []domain.ThreadID{}, nil
	}

	maxOut := f.MaxCandidates
	if maxOut <= 0 {
		maxOut = maxCandidatesDefault
	}

	queryWeights := buildQueryTermWeights(item)
	if len(queryWeights) == 0 {
		return smallRepoFallback(validThreads, maxOut), nil
	}

	type docTokens struct {
		titleFreq   map[string]int
		summaryFreq map[string]int
		titleLen    int
		summaryLen  int
	}

	docs := make([]docTokens, len(validThreads))
	docFreq := make(map[string]int)
	totalTitleLen := 0
	totalSummaryLen := 0

	for i, t := range validThreads {
		tToks := tokenizeAndStem(t.Title)
		sToks := tokenizeAndStem(t.Summary)
		tFreq := make(map[string]int, len(tToks))
		sFreq := make(map[string]int, len(sToks))
		seenInDoc := make(map[string]struct{}, len(tToks)+len(sToks))

		for _, tok := range tToks {
			tFreq[tok]++
			seenInDoc[tok] = struct{}{}
		}
		for _, tok := range sToks {
			sFreq[tok]++
			seenInDoc[tok] = struct{}{}
		}
		for tok := range seenInDoc {
			docFreq[tok]++
		}
		docs[i] = docTokens{
			titleFreq:   tFreq,
			summaryFreq: sFreq,
			titleLen:    len(tToks),
			summaryLen:  len(sToks),
		}
		totalTitleLen += len(tToks)
		totalSummaryLen += len(sToks)
	}

	nDocs := float64(len(validThreads))
	avgTitleLen := float64(totalTitleLen) / nDocs
	if avgTitleLen < 1.0 {
		avgTitleLen = 1.0
	}
	avgSummaryLen := float64(totalSummaryLen) / nDocs
	if avgSummaryLen < 1.0 {
		avgSummaryLen = 1.0
	}

	const (
		k1 = 1.2
		b  = 0.75
	)

	var matched []scoredThread
	for i, t := range validThreads {
		doc := docs[i]
		score := 0.0
		for term, qWeight := range queryWeights {
			df := float64(docFreq[term])
			if df == 0 {
				continue
			}
			idf := math.Log(1.0 + (nDocs-df+0.5)/(df+0.5))
			if tfTitle := float64(doc.titleFreq[term]); tfTitle > 0 {
				normTF := (tfTitle * (k1 + 1.0)) / (tfTitle + k1*(1.0-b+b*(float64(doc.titleLen)/avgTitleLen)))
				score += qWeight * 2.5 * idf * normTF
			}
			if tfSum := float64(doc.summaryFreq[term]); tfSum > 0 {
				normTF := (tfSum * (k1 + 1.0)) / (tfSum + k1*(1.0-b+b*(float64(doc.summaryLen)/avgSummaryLen)))
				score += qWeight * 1.0 * idf * normTF
			}
		}
		if score > 0 {
			matched = append(matched, scoredThread{summary: t, score: score})
		}
	}

	if len(matched) == 0 {
		return smallRepoFallback(validThreads, maxOut), nil
	}

	sortScoredThreads(matched)
	if len(matched) > maxOut {
		matched = matched[:maxOut]
	}

	out := make([]domain.ThreadID, len(matched))
	for i, m := range matched {
		out[i] = m.summary.ID
	}
	return out, nil
}

// VectorFinder narrows candidate threads using L2-normalized TF-IDF word, bigram, and character n-gram cosine similarity.
type VectorFinder struct {
	MaxCandidates int
}

// FindCandidates computes TF-IDF vector embeddings for the item and threads and ranks threads by cosine similarity.
func (f *VectorFinder) FindCandidates(ctx context.Context, repo string, item domain.Item, threads []domain.ThreadSummary) ([]domain.ThreadID, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("analyze", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}

	validThreads, _ := deduplicateThreads(threads)
	if len(validThreads) == 0 {
		return []domain.ThreadID{}, nil
	}

	maxOut := f.MaxCandidates
	if maxOut <= 0 {
		maxOut = maxCandidatesDefault
	}

	queryFeats := buildVectorFeatures(item.Title, strings.Join(item.Labels, " "), item.Body)
	if len(queryFeats) == 0 {
		return smallRepoFallback(validThreads, maxOut), nil
	}

	docFeats := make([]map[string]float64, len(validThreads))
	docFreq := make(map[string]int, len(queryFeats)*2)
	for feat := range queryFeats {
		docFreq[feat] = 1
	}

	for i, t := range validThreads {
		feats := buildVectorFeatures(t.Title, "", t.Summary)
		docFeats[i] = feats
		for feat := range feats {
			docFreq[feat]++
		}
	}

	totalDocs := float64(len(validThreads) + 1)
	idf := make(map[string]float64, len(docFreq))
	for feat, df := range docFreq {
		idf[feat] = 1.0 + math.Log((totalDocs+1.0)/(float64(df)+1.0))
	}

	queryVec := normalizeTFIDFVector(queryFeats, idf)
	if len(queryVec) == 0 {
		return smallRepoFallback(validThreads, maxOut), nil
	}

	var matched []scoredThread
	for i, t := range validThreads {
		docVec := normalizeTFIDFVector(docFeats[i], idf)
		sim := cosineSimilarity(queryVec, docVec)
		if sim > 0.01 {
			matched = append(matched, scoredThread{summary: t, score: sim})
		}
	}

	if len(matched) == 0 {
		return smallRepoFallback(validThreads, maxOut), nil
	}

	sortScoredThreads(matched)
	if len(matched) > maxOut {
		matched = matched[:maxOut]
	}

	out := make([]domain.ThreadID, len(matched))
	for i, m := range matched {
		out[i] = m.summary.ID
	}
	return out, nil
}

// LLMFinder narrows candidate threads by prompting the configured Gemini model with compact thread summaries.
type LLMFinder struct {
	client        *Client
	MaxCandidates int
}

// ValidateConfig verifies that the underlying LLM client is configured with credentials.
func (f *LLMFinder) ValidateConfig() error {
	if f == nil || f.client == nil {
		return domain.NewError(
			domain.ErrorCodeNotConfigured,
			"analyze",
			"LLM client for candidate discovery is not configured.",
			"Set AUTOMAIN_LLM_API_KEY or GEMINI_API_KEY, or use --finder fts / --finder vector.",
			nil,
		)
	}
	return f.client.ValidateConfig()
}

const candidateFinderSystemPrompt = `You are a repository intelligence assistant. Given a GitHub item (issue, pull request, or discussion) and a list of existing threads in the repository, select the IDs of all candidate threads that could plausibly relate to the item (same problem, proposed fix, architectural decision, dependency/blocker, related context, or duplicate). Return JSON with field "candidate_thread_ids" containing an array of matching thread IDs ordered from most relevant to least relevant.`

var candidateFinderResponseSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"candidate_thread_ids": map[string]any{
			"type": "ARRAY",
			"items": map[string]any{
				"type": "STRING",
			},
		},
	},
	"required": []string{"candidate_thread_ids"},
}

// FindCandidates prompts the LLM to select candidate threads for item from threads.
func (f *LLMFinder) FindCandidates(ctx context.Context, repo string, item domain.Item, threads []domain.ThreadSummary) ([]domain.ThreadID, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.AsError("analyze", err)
	}
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInvalidInput,
			"analyze",
			err.Error(),
			"Pass the repository in owner/name format.",
			err,
		)
	}

	validThreads, byID := deduplicateThreads(threads)
	if len(validThreads) == 0 {
		return []domain.ThreadID{}, nil
	}

	if err := f.ValidateConfig(); err != nil {
		return nil, err
	}

	maxOut := f.MaxCandidates
	if maxOut <= 0 {
		maxOut = maxCandidatesDefault
	}

	promptPayload := struct {
		Repository string                 `json:"repository"`
		MaxResults int                    `json:"max_results"`
		Item       map[string]any         `json:"item"`
		Threads    []domain.ThreadSummary `json:"threads"`
	}{
		Repository: repo,
		MaxResults: maxOut,
		Item: map[string]any{
			"number": item.Number,
			"kind":   item.Kind,
			"state":  item.State,
			"title":  item.Title,
			"labels": item.Labels,
			"body":   truncateString(item.Body, 4000),
		},
		Threads: validThreads,
	}

	promptBytes, err := json.MarshalIndent(promptPayload, "", "  ")
	if err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeInternal,
			"analyze",
			fmt.Sprintf("Failed to serialize candidate discovery prompt: %v", err),
			"Verify item and thread summary fields.",
			err,
		)
	}

	respJSON, err := f.client.generateJSON(ctx, candidateFinderSystemPrompt, string(promptBytes), candidateFinderResponseSchema)
	if err != nil {
		return nil, err
	}

	rawIDs, err := parseCandidateThreadIDs(respJSON)
	if err != nil {
		return nil, domain.NewError(
			domain.ErrorCodeExternalUnavailable,
			"analyze",
			fmt.Sprintf("Failed to decode candidate thread IDs from LLM response: %v", err),
			"Verify that AUTOMAIN_LLM_MODEL returns valid JSON.",
			err,
		)
	}

	seen := make(map[domain.ThreadID]struct{}, len(rawIDs))
	out := make([]domain.ThreadID, 0, len(rawIDs))
	for _, raw := range rawIDs {
		tid := domain.ThreadID(strings.TrimSpace(raw))
		if tid == "" {
			continue
		}
		if _, exists := byID[tid]; !exists {
			continue
		}
		if _, dup := seen[tid]; dup {
			continue
		}
		seen[tid] = struct{}{}
		out = append(out, tid)
		if len(out) >= maxOut {
			break
		}
	}

	return out, nil
}

func parseCandidateThreadIDs(data []byte) ([]string, error) {
	var directSlice []string
	if err := json.Unmarshal(data, &directSlice); err == nil && directSlice != nil {
		return directSlice, nil
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, fmt.Errorf("unexpected candidate thread JSON structure")
	}

	for _, key := range []string{"candidate_thread_ids", "thread_ids", "candidates"} {
		if rawVal, exists := rawMap[key]; exists {
			trimmed := strings.TrimSpace(string(rawVal))
			if trimmed == "null" {
				return []string{}, nil
			}
			var ids []string
			if err := json.Unmarshal(rawVal, &ids); err == nil {
				return ids, nil
			}
		}
	}

	if rawMatched, exists := rawMap["matched_threads"]; exists {
		var matched []struct {
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(rawMatched, &matched); err == nil {
			ids := make([]string, 0, len(matched))
			for _, m := range matched {
				if strings.TrimSpace(m.ThreadID) != "" {
					ids = append(ids, strings.TrimSpace(m.ThreadID))
				}
			}
			return ids, nil
		}
	}

	if len(rawMap) == 0 {
		return []string{}, nil
	}

	return nil, fmt.Errorf("unexpected candidate thread JSON structure")
}

func smallRepoFallback(threads []domain.ThreadSummary, maxOut int) []domain.ThreadID {
	const smallRepoThreshold = 5
	if len(threads) > smallRepoThreshold {
		return []domain.ThreadID{}
	}
	limit := len(threads)
	if limit > maxOut {
		limit = maxOut
	}
	scored := make([]scoredThread, len(threads))
	for i, t := range threads {
		scored[i] = scoredThread{summary: t, score: 0}
	}
	sortScoredThreads(scored)
	out := make([]domain.ThreadID, limit)
	for i := 0; i < limit; i++ {
		out[i] = scored[i].summary.ID
	}
	return out
}

var commonStopwords = map[string]struct{}{
	"a": {}, "about": {}, "all": {}, "also": {}, "an": {}, "and": {}, "any": {},
	"are": {}, "as": {}, "at": {}, "be": {}, "been": {}, "but": {}, "by": {},
	"can": {}, "could": {}, "did": {}, "do": {}, "does": {}, "for": {}, "from": {},
	"had": {}, "has": {}, "have": {}, "how": {}, "if": {}, "in": {}, "into": {},
	"is": {}, "it": {}, "its": {}, "just": {}, "more": {}, "no": {}, "not": {},
	"of": {}, "on": {}, "one": {}, "only": {}, "or": {}, "other": {}, "our": {},
	"out": {}, "should": {}, "so": {}, "some": {}, "such": {}, "than": {},
	"that": {}, "the": {}, "their": {}, "them": {}, "then": {}, "there": {},
	"these": {}, "they": {}, "this": {}, "to": {}, "up": {}, "use": {}, "used": {},
	"using": {}, "was": {}, "we": {}, "were": {}, "what": {}, "when": {},
	"where": {}, "which": {}, "who": {}, "will": {}, "with": {}, "would": {},
}

func buildQueryTermWeights(item domain.Item) map[string]float64 {
	weights := make(map[string]float64)
	for _, tok := range tokenizeAndStem(item.Title) {
		weights[tok] += 3.0
	}
	for _, label := range item.Labels {
		for _, tok := range tokenizeAndStem(label) {
			weights[tok] += 2.0
		}
	}
	for _, tok := range tokenizeAndStem(truncateString(item.Body, 4000)) {
		weights[tok] += 1.0
	}
	return weights
}

func tokenizeAndStem(text string) []string {
	lower := strings.ToLower(text)
	var tokens []string
	var buf strings.Builder

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		word := buf.String()
		buf.Reset()
		if len(word) < 2 {
			return
		}
		if _, stop := commonStopwords[word]; stop {
			return
		}
		stemmed := stemToken(word)
		if len(stemmed) >= 2 {
			tokens = append(tokens, stemmed)
		}
	}

	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	// Emit #<number> tokens for explicit issue/PR/discussion references without indexing markdown ## headers.
	refs := ExtractReferencedItemNumbers("", lower)
	if len(refs) > 0 {
		nums := make([]int, 0, len(refs))
		for num := range refs {
			nums = append(nums, num)
		}
		sort.Ints(nums)
		for _, num := range nums {
			tokens = append(tokens, "#"+strconv.Itoa(num))
		}
	}

	return tokens
}

func stemToken(word string) string {
	if len(word) <= 3 {
		return word
	}
	switch {
	case strings.HasSuffix(word, "ies") && len(word) > 4:
		return word[:len(word)-3] + "y"
	case strings.HasSuffix(word, "ing") && len(word) > 5:
		return word[:len(word)-3]
	case strings.HasSuffix(word, "ed") && len(word) > 4:
		return word[:len(word)-2]
	case strings.HasSuffix(word, "es") && len(word) > 4:
		return word[:len(word)-2]
	case strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") && len(word) > 3:
		return word[:len(word)-1]
	default:
		return word
	}
}

func buildVectorFeatures(title, labels, body string) map[string]float64 {
	feats := make(map[string]float64)

	addSection := func(text string, weight float64) {
		toks := tokenizeAndStem(text)
		for i, tok := range toks {
			feats["w:"+tok] += weight
			if i+1 < len(toks) {
				feats["b:"+tok+"_"+toks[i+1]] += weight * 0.75
			}
			runes := []rune(tok)
			if len(runes) >= 4 && !strings.HasPrefix(tok, "#") {
				for j := 0; j+3 <= len(runes); j++ {
					feats["c3:"+string(runes[j:j+3])] += weight * 0.25
				}
			}
		}
	}

	addSection(title, 3.0)
	addSection(labels, 2.0)
	addSection(truncateString(body, 4000), 1.0)
	return feats
}

func normalizeTFIDFVector(tf map[string]float64, idf map[string]float64) map[string]float64 {
	if len(tf) == 0 {
		return nil
	}
	vec := make(map[string]float64, len(tf))
	normSq := 0.0
	for feat, count := range tf {
		if count <= 0 {
			continue
		}
		weight := math.Log(1.0+count) * idf[feat]
		vec[feat] = weight
		normSq += weight * weight
	}
	if normSq <= 0 {
		return nil
	}
	norm := math.Sqrt(normSq)
	for feat, w := range vec {
		vec[feat] = w / norm
	}
	return vec
}

func cosineSimilarity(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	dot := 0.0
	for feat, wa := range a {
		if wb, ok := b[feat]; ok {
			dot += wa * wb
		}
	}
	return dot
}

func truncateString(s string, maxChars int) string {
	trimmed := strings.TrimSpace(s)
	if maxChars <= 0 || len(trimmed) <= maxChars {
		return trimmed
	}
	runes := []rune(trimmed)
	if len(runes) <= maxChars {
		return trimmed
	}
	return string(runes[:maxChars])
}
