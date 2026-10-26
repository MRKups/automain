# Jev decision plumbing

[Architecture](../README.md) | [Configuration](../config/README.md)

Jev is TypeSafe's System One **decision model**, not a chat model. It evaluates
supplied state against typed questions. It returns decisions and probabilities,
not prose, reasoning traces, explanations, or newly written evidence.
TypeSafe describes its training objective as RLCD (reinforcement learning for
calibrated decisions), rather than optimizing generated replies for preference.
That describes the intended model behavior, not an accuracy guarantee for our data.

The intended split is:

```text
Gemini embeddings -> vector shortlist -> Jev judgments -> policy in Go
                                                       -> source-backed results
                                                       -> optional LLM for writing
```

This change implements the **decision transport and application boundary**.
`automain decide` and `POST /api/decide` accept explicitly supplied state/questions.
They do not automatically run search, load item pairs, create threads, persist
decisions, set thresholds, generate text, or call legacy `analyze`. The retrieval
and decision stages are independently usable; their automatic composition and
relationship policy are deliberately not chosen yet.

## What Jev does

Every request contains one `state` (string, object, or array) and a map of named
questions. Related records can be nested in state, with instructions referring to
backticked paths such as `source.text` and `candidate.text`. Question IDs route
answers in code; Jev does not see those IDs, so put the complete question in its
instructions. Instructions and criterion descriptions may be strings, objects,
or arrays. Choice descriptions may also be null when option names suffice.

| Primitive | Request criteria | Answer |
| --- | --- | --- |
| Noul | Optional descriptions of exactly `true` and `false` | `noul`: probability of yes, 0-1 |
| Choice | Map of 2-255 named, unordered options | Selected `choice`, all `probabilities`, `confidence` |
| Score | Ordered array of 2-10 descriptive levels | Weighted `score`, indexed `legend`, all `probabilities`, `confidence` |

Questions sharing a state are evaluated independently, in parallel. Ask several
atomic questions together rather than one broad "analyze these items" question.
One answer is not hidden input to the next. If another judgment truly needs a
previous answer to construct its state/options, code must make a second call.

**Probability, confidence, and strength are different:**

- Noul 0.5 means uncertainty about yes/no, not a medium-strength relationship.
  There is no separate Noul confidence field; we do not invent one.
- Choice confidence summarizes concentration of the option distribution. It is
  not the selected option's probability and is not a guarantee of correctness.
- Score is the probability-weighted level index, from 0 to `len(criteria)-1`.
  A three-level score can be 1.43. Different distributions can have the same mean;
  keep the distribution and confidence, not just a rounded label.
- Calibrated probabilities are a model property to evaluate on our own data, not
  proof that an individual answer is correct. Thresholds depend on question
  wording, model version, examples, and the consequences of a false link.

## OpenRouter wire format

- Default model: **`typesafe/jev-1.14`**, pinned rather than moving with an alias.
- Full endpoint: **`POST https://openrouter.ai/api/alpha/decisions`**.
- Authentication: `Authorization: Bearer <OpenRouter key>`.
- Body: `{"model":"typesafe/jev-1.14","state":...,"questions":{...}}`.
- Response: `model`, `answers`, `usage`, plus optional `id` and `provider`.
- Preserve the returned model revision, input/output token counts, and optional
  `usage.cost` in USD. Do not hardcode pricing; consult OpenRouter for live prices.

This is **not** `/api/v1/chat/completions` or `/api/v1/embeddings`. TypeSafe's
direct API uses different names (`jev-1.14.0`, `jev-latest`) and `/v1/systemone`.
OpenRouter also offers a TypeSafe-SDK-compatible System One surface, but this Go
adapter uses the Decisions API with ordinary HTTP and no additional SDK.

Credentials resolve from `AUTOMAIN_DECISION_API_KEY`, then `OPENROUTER_API_KEY`.
Embedding-specific, TypeSafe-direct, and legacy LLM keys are not fallbacks.
`AUTOMAIN_DECISION_MODEL` also accepts `typesafe/jev-1.13` and
`~typesafe/jev-latest`. An alias can change answers without a code change; prefer
the pinned release when evaluating questions/thresholds. Unrelated models fail
configuration validation. No model substitution or LLM fallback is implemented.

## Try the plumbing

The checked-in [example request](example.json) demonstrates all three primitives
over two short records. These are illustrative questions, **not our final
relationship taxonomy or decision policy**.

```sh
automain decide --input internal/decision/example.json --json

curl -sS http://127.0.0.1:8080/api/decide \
  -H 'Content-Type: application/json' \
  --data-binary @internal/decision/example.json
```

No repository is required; callers provide their own bounded state. API clients
cannot override model, endpoint, credentials, or provider routing in the body.
`--json` returns the normal application envelope with command `decide`; without
it the CLI prints just the typed result as JSON. HTTP also accepts `/api/v1/decide`.

The common binary factory still opens/migrates both configured SQLite files.
The `Decide` operation itself has no storage reads or writes and does not take
mutation ownership. Nothing is cached: every explicit successful call is a new
paid request. Save the returned output yourself if experimenting with thresholds.

## Safeguards and failures

- **30,000-byte total serialized request budget including model**, deliberately
  conservative relative to the OpenRouter guide's roughly 32KB long-context
  switch. The CLI file/HTTP body also has a 30,000-byte read cap. These are byte
  budgets, not token counts or a claim about Jev's full context capacity.
- No silent truncation, summarization, automatic splitting, or long-context
  header. Select passages or split requests explicitly when context is too large.
  Upstream docs describe different normal/long-context limits; we do not rely on
  the advertised 262K maximum to send entire repository histories.
- UTF-8 JSON, finite numbers, unique JSON keys, and at most 64 container nesting
  levels. Unknown input fields are rejected. State and instructions must have
  supported shapes; questions must have valid primitive-specific criteria.
- Only Noul, Choice, and Score are implemented. No Box primitive or image-asset
  helpers. Use text/structured repository records for this integration.
- Exactly one correctly typed answer per requested ID. Missing/extra answers,
  unexpected models, absent/null/invalid numeric fields, out-of-range confidence,
  and incomplete or invalid distributions fail the entire response.
- Choice must select a supplied maximum-probability option. Score must be in
  range, have the supplied legend, and agree with its weighted distribution.
  Distribution sums allow 0.01 rounding tolerance; score agreement allows 0.02.
  Invalid output is never repaired or renormalized. Confidence is range-checked,
  not recomputed. Optional new upstream response fields are tolerated.
- A pinned release accepts its exact ID or a hyphen-suffixed revision. The moving
  alias accepts a resolved `typesafe/jev-*` model. The actual response ID is kept.
- Responses are capped at 1 MiB. No automatic retries or redirects, including on
  rate limits; no replay of authenticated requests at a redirected endpoint.
- HTTPS endpoints only, except loopback HTTP for offline checks. URL credentials,
  query strings, and fragments are rejected. Endpoints are visible in status.
- Network failures, cancellation/deadlines, provider statuses, and malformed
  responses return safe `external_unavailable` errors. Error output never echoes
  provider bodies, question IDs, or source text. Missing keys are `not_configured`;
  bad settings/input are `invalid_input`.

`status` reports adapter presence, model, endpoint, and key presence without
contacting OpenRouter. The server has no built-in authentication; exposing this
endpoint allows paid calls using the server's key. Keep it loopback/protected.

## Next policy decisions, intentionally open

- How vector passages become item-pair context, retaining source IDs, exact text,
  offsets, content fingerprints, and evidence from both sides.
- Separate atomic checks for same problem, a proposed fix, a dependency in each
  direction, duplicate reports, and supporting/contradicting context. Several may
  hold simultaneously; a single Choice should not force one label when the task
  is multi-label. A Choice can still select among mutually exclusive alternatives.
- How to represent "unrelated" and "insufficient evidence", route uncertainty to
  review, aggregate passage judgments, and avoid treating missing context as no.
- Which probabilities become persisted relationships, and how to store the model,
  question revision, source snapshot, distributions, and review status with them.
- Source-backed output for decisions, with an LLM used only when writing is needed.

Jev cannot invent a written explanation or quote. We should select/verify known
source spans in code rather than ask it to generate evidence. Source text remains
untrusted data; model judgments are not a security boundary. The old generative
analysis schema mixes classification with summaries/explanations, so this adapter
does not pretend to implement that interface or silently reuse its persistence.

TypeSafe's 1.13-specific limitations guide warns about literal wording, arithmetic,
date comparisons, multi-hop reasoning, irrelevant context, adversarial content,
and Choice option-order bias. Keep deterministic work in Go and turn those cases
into evaluation fixtures; do not assume a newer release has resolved them all.

## Sources studied

Reviewed October 26, 2026. OpenRouter governs this adapter's wire contract;
TypeSafe supplies the conceptual and question-design guidance. Some upstream
pages disagree about media/context capabilities; this integration stays with the
shared text primitives and the conservative request budget above.

- [OpenRouter Jev hub](https://openrouter.ai/docs/guides/community/jev)
- [OpenRouter Decisions API reference](https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-questions-and-answers-request)
- [TypeSafe documentation index](https://docs.typesafe.ai/llms.txt)
- [System One](https://docs.typesafe.ai/concepts/system-one), [state](https://docs.typesafe.ai/concepts/state), and [workflow design](https://docs.typesafe.ai/concepts/how-to-build-with-system-one)
- [Training objective](https://docs.typesafe.ai/introduction/machine-learning-primer) and [Jev 1.13 limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13)
- [Primitives](https://docs.typesafe.ai/primitives): [Noul](https://docs.typesafe.ai/primitives/noul), [Choice](https://docs.typesafe.ai/primitives/choice), [Score](https://docs.typesafe.ai/primitives/score)
- [Confidence](https://docs.typesafe.ai/confidence), [models/context](https://docs.typesafe.ai/models), and [request rules/errors](https://docs.typesafe.ai/api/errors)
- [Entity alignment](https://docs.typesafe.ai/cookbooks/entity_alignment) and [classifying retrieved passages](https://docs.typesafe.ai/cookbooks/classifying_rag_passages)

Run `task check:decisions` for offline fixtures. They check plumbing and failure
handling, not semantic accuracy, calibration, live availability, price, or latency.
