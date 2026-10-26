# OpenRouter embedding adapter

[Architecture](../README.md) | [Configuration](../config/README.md) |
[Retrieval](../retrieval/README.md)

[`openrouter.go`](openrouter.go) implements `domain.EmbeddingProvider` using exactly
**`google/gemini-embedding-2`** through OpenRouter. There is no alternative model,
synthetic-vector fallback, or local inference service. This client is independent
of the legacy relationship-analysis client.

## Requests and cache identity

- Default endpoint: `POST https://openrouter.ai/api/v1/embeddings`.
- `AUTOMAIN_EMBEDDING_MODEL` defaults to `google/gemini-embedding-2`; only that value is accepted.
- Key: `AUTOMAIN_EMBEDDING_API_KEY`, falling back to `OPENROUTER_API_KEY`.
- Full-size output: **3072 dimensions** by default; configurable from 128 to 3072.
- JSON includes `model`, `input` (1-32 strings), `dimensions`, and
  `encoding_format: "float"`. Authorization uses a bearer token.
- Document text comes from retrieval as `title: ... | text: ...`.
- Queries receive the prefix `task: search result | query: `.

Creating the client makes no network requests. Request timeout uses the configured
timeout. Embedding-space identity includes provider/model/dimensions and a
revision derived from the endpoint and input recipe. Endpoint changes therefore
cannot share cached vectors accidentally. Changes to model versions, task
instructions, or formatting require a revision change, not reuse of old vectors.

## Input safety

The adapter rejects empty/invalid UTF-8 inputs, unsupported purposes, oversized
batches, and complete formatted inputs above **8000 bytes** before HTTP submission.
This is a conservative byte budget, not an 8000-token counter. Query prefixes and
document title context count toward it. Inputs are never automatically truncated.
Source splitting lives in [retrieval](../retrieval/README.md#long-inputs-and-chunking),
not in this adapter. If any input is invalid, the whole request is rejected.

## Response validation

A successful response must be a JSON object with `object: "list"` for the exact
requested model, with exactly one vector per input. Indices must be present,
unique, and in range;
responses are reordered into input order. Vectors must have the configured
dimension, finite numeric entries, no nulls, and a nonzero norm. Responses larger
than 32 MiB are rejected. No batch is saved unless validation succeeds.

## Failures and cost

There are **no automatic retries**, and redirects are not followed. HTTP failures
return `external_unavailable` with a status-specific recovery hint for credentials,
credit, model availability, rate limits, or invalid requests. Provider error bodies
are not echoed because they can contain source text or credentials. Missing keys
return `not_configured`; invalid local settings/inputs return `invalid_input`.

**Known defect (R6):** if the operation context expires during an embedding call,
the adapter can return `internal_error` (HTTP 500) rather than `external_unavailable`.
This misclassifies an upstream timeout and supplies an unhelpful recovery hint.
See the [review follow-up](../../QDRANT_IMPLEMENTATION_PLAN.md#review-follow-up).

Re-running indexing reuses already saved batches. A provider may still charge for
a request that completed remotely but failed locally before its vectors were
saved. Search on an indexed space makes a fresh query request each time.

Documents and queries are sent to OpenRouter and its upstream provider. A separate
local vector file is not a promise that source text stays on this machine.

See [offline checks](../../scripts/README.md) for mock-API coverage. Those checks
verify integration and failure handling, not live availability or semantic quality.
