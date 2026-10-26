# HTTP API and embedded assets

[Architecture](../README.md) | [CLI](../cli/README.md)

```sh
automain serve --repo owner/name --listen 127.0.0.1:8080
```

The server exposes the shared application service and serves the embedded Svelte
application at `/`. API paths below also accept an `/api/v1` prefix instead of
`/api`. Repository query parameters accept `repo` or `repository`; operations can
fall back to the configured repository.

## Endpoints

| Method/path | Inputs |
| --- | --- |
| `GET /healthz`, `/api/health` | Liveness |
| `GET /readyz`, `/api/ready` (`/api/readyz` alias) | Local storage and Qdrant readiness, no model calls; failures return 503 |
| `GET /api/version`, `/api/status` | Version/configuration and local status |
| `GET /api/repositories` (`/api/repos`) | Imported repositories |
| `GET /api/stats`, `/api/graph` | `repo` |
| `POST /api/sync` | `repository`, `limit`, `since` (RFC3339), `full` |
| `POST /api/analyze` | `repository`, `item_number`, `limit`, `reanalyze`, `finder` |
| `GET /api/index/status` | `repo` |
| `POST /api/index/prepare` | `repository`; no limit |
| `POST /api/index/embed` | `repository`, `limit` (passages, default 100, 1-10000) |
| `POST /api/index/reconcile` | `repository`; no limit or model calls |
| `POST /api/index/migrate` | `repository`, explicit local legacy `path`, `limit` (vector writes, default 100, 1-10000); read-only source, no model calls |
| `GET /api/search` | `repo`, `q` (`query` alias), `limit` (default 10, 1-100) |
| `POST /api/search` | `repository`, `query`, `limit` |
| `GET /api/similar`, `POST /api/similar` | `repository`, exactly one non-empty `chunk_id` or positive `item_number`, `limit` (candidate items, default 10, 1-100) |
| `POST /api/decide` | `state`, `questions`; no repo required, model/key configured server-side |
| `GET /api/items` | `repo`, `kind`, `state`, `dirty`, `limit` |
| `GET /api/items/{number}` | `repo`; `/api/items/show?number=...` also supported |
| `GET /api/threads` | `repo`, `status` |
| `GET /api/threads/{id}` | `/api/threads/show?id=...` also supported |
| `GET /api/relationships` | `repo`, `type`, `verified`, `thread`, `item` |
| `GET /api/relationships/{id}` | `/api/relationships/show?id=...` also supported |
| `POST /api/relationships/verify` | `id`, `verified`; also `/api/relationships/{id}/verify` |

POST field names refer to JSON bodies. Retrieval endpoints also accept query
parameters; non-empty query repository/search text and explicit query limits take
precedence over body values. Migration requires `path` in the JSON body, not a configured legacy
default. Most read endpoints also accept HEAD; search and similar accept GET/POST only.

Relationship verification also accepts PATCH/PUT on the verification paths, or
POST/PATCH/PUT directly to `/api/relationships/{id}`. It defaults to marking the
record verified; send `{"verified":false}` to clear it. This changes a review
flag, not the underlying relationship claim. See
[review limitations](../analysis/README.md#current-limits).

## Retrieval examples

```sh
curl -sS http://127.0.0.1:8080/api/index/prepare \
  -H 'Content-Type: application/json' -d '{"repository":"owner/name"}'

curl -sS http://127.0.0.1:8080/api/index/embed \
  -H 'Content-Type: application/json' -d '{"repository":"owner/name","limit":100}'

curl -sS --get http://127.0.0.1:8080/api/search \
  --data-urlencode 'repo=owner/name' --data-urlencode 'q=stale cache'
```

Preparation makes no model calls. Embedding and indexed-space search use the
server's OpenRouter key, not a key supplied per request. Search returns exact
source passages and cosine scores, not relationships. See
[chunking and freshness](../retrieval/README.md).

Similarity uses stored vectors without an embedding key or model calls, excludes
the source item, and groups candidate items with supporting passages. A truncated
result reports a bounded subset of source passages, not complete item coverage.

## Responses and errors

Success: `{"ok":true,"command":"search","data":...}`. Failure:
`{"ok":false,"command":"search","error":{"code":"...","operation":"...","message":"...","recovery":"..."}}`.
`recovery` is optional. API responses are JSON, not the UI HTML fallback.

| Error code | HTTP status |
| --- | --- |
| `invalid_input` | 400 |
| `not_found` | 404 |
| `not_configured` | 503 |
| `external_unavailable` | 502 |
| Other/internal | 500 |

Unsupported methods return 405. JSON bodies are limited to 1 MiB; malformed or
oversized bodies return invalid-input errors. Retrieval endpoints (`index`,
`search`, and `similar`) reject unknown or repeated query parameters, unknown or
duplicate JSON fields, null field values, and non-object or invalid UTF-8 JSON
bodies before invoking application operations. GET/HEAD retrieval requests do not
accept bodies. Omit optional fields to use defaults. Other
endpoints generally tolerate unknown JSON fields.
**`/api/decide` is stricter:** Content-Type must be `application/json`, the
body is limited to 30000 bytes, and unknown fields/duplicate keys/invalid question
shapes are rejected. The complete serialized provider request including model
must also fit 30000 bytes. See [Jev requests](../decision/README.md#safeguards-and-failures).
Upstream statuses such as 429 are represented through the external-error mapping,
not necessarily forwarded as the same HTTP status.

## Deployment limits

- **No authentication or authorization.** Keep the default loopback binding or
  protect the service externally. Exposing it permits reads, writes, and paid
  model calls. Migration also permits reading a caller-selected local legacy
  SQLite file accessible to the server. Setting `PORT` without an explicit address binds all interfaces.
- Operations run synchronously, not as background jobs. Mutations are serialized
  per service, including provider calls; queued requests can leave on cancellation
  without starting downstream work. See [mutation ownership](../app/README.md#concurrency-and-partial-progress).
- `--timeout` bounds request contexts and socket body reads (default 60s), including
  decision, retrieval, and legacy JSON bodies. Body-read timeouts return
  `external_unavailable` (502) when a response can still be written; malformed JSON
  remains `invalid_input` (400). The deadline also bounds unread-body cleanup.
  The HTTP write timeout is independently fixed at **120s**. Raising `--timeout`
  does not raise that limit; prefer bounded batches or the CLI for long work.
- Read-header timeout is 10s, idle timeout 60s, shutdown grace 10s.
- Liveness is not a live GitHub/OpenRouter credential or availability check.
- Readiness checks local storage and Qdrant with a deadline of at most five seconds,
  without model calls. Qdrant failure does not change liveness or disable offline operations.
- These read bounds also apply when embedding `server.New` in a standard Go HTTP
  server. Network middleware must expose response-controller methods or `Unwrap`.
  In-memory response writers without deadline support still accept finite bodies;
  arbitrary blocking readers without socket deadline support cannot be interrupted.

`POST /api/decide` makes a paid Jev request and returns typed decisions, full
probabilities, model revision, and usage/cost. It does not write relationships or
run search/legacy analysis. It uses the server's key and does not take the storage
mutation ownership. No automatic retry, redirect, or LLM fallback is made.

Rebuild frontend assets and then the binary to change the served UI. See
[web development](../../web/README.md).

Provider timeout errors retain their operation and recovery details through HTTP;
embedding timeouts return `external_unavailable` (502), not an internal error.
