# Configuration

[Architecture](../README.md)

Settings come from the current working directory's `.env`, the process environment,
and CLI flags. Non-empty process variables take precedence over `.env`; empty
variables can be filled from it. CLI flags override the resolved configuration.
Missing `.env` files are fine. Read errors produce a warning on stderr.

## Core settings

| Environment variable | Default | CLI override |
| --- | --- | --- |
| `AUTOMAIN_DB_PATH` | `./data/automain.db` | `--db` |
| `AUTOMAIN_INDEX_DB_PATH` | `index.db` beside the selected source DB | `--index-db` |
| `AUTOMAIN_VECTOR_DB_PATH` | Legacy source path only, deprecated | `--vector-db` |
| `AUTOMAIN_REPO` | None | `--repo owner/name` |
| `AUTOMAIN_FINDER` | `llm` | `--finder llm\|fts\|vector` |
| `AUTOMAIN_LISTEN_ADDR` | `127.0.0.1:8080` | `--listen` |
| `GITHUB_TOKEN` | None | None |

If the listen address is unset and `PORT` is set, the address becomes
`0.0.0.0:$PORT`, which exposes the unauthenticated server beyond loopback.
`--timeout` accepts a Go duration such as `60s` or `5m`, defaults to 60 seconds,
and must be positive. There is no timeout environment variable.

Source, catalog, and explicitly configured legacy paths must be different,
including symlink/hard-link aliases. The legacy file is never opened at startup
and is never the catalog. Migration requires an explicit `index migrate --from
<path>` even when the legacy setting is present. See
[storage](../storage/README.md) for ownership and rebuild behavior.

## Qdrant

| Environment variable | Default |
| --- | --- |
| `AUTOMAIN_QDRANT_HOST` | `127.0.0.1` |
| `AUTOMAIN_QDRANT_PORT` | `6334`, gRPC; valid range 1-65535 |
| `AUTOMAIN_QDRANT_TLS` | `false` |
| `AUTOMAIN_QDRANT_API_KEY` | None |
| `AUTOMAIN_QDRANT_COLLECTION_PREFIX` | `automain_` |

The host must be a hostname or literal IP, not a URL, host:port, or credentials.
TLS is required for remote/public addresses and multi-label hostnames. Plaintext
is allowed for loopback/private literal IPs and single-label deployment names
such as `qdrant`. No DNS lookup is used to decide whether a name is private.
Prefixes accept 1-128 ASCII letters, digits, underscores, or hyphens.

Configuration validation performs no network or credential probes. Non-vector
operations do not require Qdrant availability or a Qdrant key. The API key is
reported only as a presence boolean. See [retrieval](../retrieval/README.md) for
remote failure and recovery behavior.

## Embeddings

The provider and model are fixed: **OpenRouter**, **`google/gemini-embedding-2`**.
The example env files explicitly populate the model, 3072 dimensions, OpenRouter
endpoint, and source/catalog paths. With an explicit catalog path, changing `--db`
alone does not move the catalog; also override `--index-db` or unset its env value.

| Environment variable | Behavior |
| --- | --- |
| `AUTOMAIN_EMBEDDING_MODEL` | Defaults to `google/gemini-embedding-2`; any other model is rejected |
| `AUTOMAIN_EMBEDDING_API_KEY` | Optional dedicated key; takes precedence |
| `OPENROUTER_API_KEY` | Embedding key when no dedicated key is set |
| `AUTOMAIN_EMBEDDING_DIMENSIONS` | Integer 128-3072; default 3072 |
| `AUTOMAIN_EMBEDDING_BASE_URL` | Default `https://openrouter.ai/api/v1` |

The endpoint appends `/embeddings`. HTTPS is required except for loopback HTTP
fixture servers. URL credentials, queries, and fragments are rejected. Changing
dimensions or the endpoint selects a different embedding cache space; existing
vectors are not reused across spaces. Model/input recipe changes also require a
new space revision. See [embedding requests](../embedding/README.md).

`status`, `index status`, `index prepare`, `index reconcile`, `index migrate`, and
`similar` need no embedding key. `index embed` and `search` do. Direct
Google/OpenAI analysis keys do not enable embeddings.

## Jev decisions

| Environment variable | Behavior |
| --- | --- |
| `AUTOMAIN_DECISION_MODEL` | Default `typesafe/jev-1.14`; also accepts `typesafe/jev-1.13` and `~typesafe/jev-latest` |
| `AUTOMAIN_DECISION_ENDPOINT` | Full endpoint, default `https://openrouter.ai/api/alpha/decisions` |
| `AUTOMAIN_DECISION_API_KEY` | Optional dedicated key, taking precedence over `OPENROUTER_API_KEY` |
| `OPENROUTER_API_KEY` | Decision key when no dedicated decision key is set |

Both env templates prepopulate the pinned Jev model and full endpoint. This is
not the embedding `/api/v1` base URL; nothing is appended to it. HTTPS is required
except for loopback HTTP fixtures. URL credentials, queries, and fragments are
rejected. A moving alias changes model behavior without a config change; use a
pinned model when evaluating questions/thresholds. Status needs no decision key.
Dedicated embedding and legacy LLM keys do not enable decisions. See
[Jev decisions](../decision/README.md) for input limits, output semantics, and
the separation between this plumbing and later relationship policy.

## Legacy relationship analysis

This configuration affects legacy `analyze`, not semantic search or Jev decisions.
`AUTOMAIN_LLM_PROVIDER` accepts `gemini` (default), `openai`, or `deepseek`.
`google` aliases Gemini; `openai-compatible` aliases OpenAI.

Common overrides take precedence over provider variables:

- Key: `AUTOMAIN_LLM_API_KEY`.
- Model: `AUTOMAIN_LLM_MODEL`.
- Base URL: `AUTOMAIN_LLM_API_URL`, then `AUTOMAIN_LLM_BASE_URL`.

Without those overrides, the first non-empty value is used:

| Provider | Key fallback order | Model fallback order | Default model |
| --- | --- | --- | --- |
| Gemini | `GEMINI_API_KEY` | `GEMINI_MODEL` | `gemini-2.5-flash` |
| OpenAI | `OPENAI_API_KEY`, `DEEPSEEK_API_KEY` | `OPENAI_MODEL` | `gpt-4o-mini` |
| DeepSeek | `DEEPSEEK_API_KEY`, `OPENAI_API_KEY` | `DEEPSEEK_MODEL`, `OPENAI_MODEL` | `deepseek-chat` |

Provider URL variables are tried in this order:

- Gemini: `GEMINI_API_URL`, `GEMINI_BASE_URL`; default
  `https://generativelanguage.googleapis.com/v1beta`.
- OpenAI: `OPENAI_API_URL`, `OPENAI_BASE_URL`; default `https://api.openai.com/v1`.
- DeepSeek: `DEEPSEEK_API_URL`, `DEEPSEEK_BASE_URL`, `OPENAI_API_URL`,
  `OPENAI_BASE_URL`; default `https://api.deepseek.com`.

The cross-provider key fallbacks are existing behavior, not model fallback.
Check the resolved provider/model/URL in `status` before running analysis.

## Secrets and validation

Use [.env.example](../../.env.example) or [env.example](../../env.example) as a
starting point; never commit real keys.
Status exposes key-presence booleans, not key values. Do not put secrets in URLs:
configured endpoints are visible in status. Embedding and Jev errors do not echo provider
response bodies. Other adapters have separate error handling.

Configuration invariants are checked even for commands that do not use a model.
A malformed dimension, finder, decision model, or URL setting can therefore block `status`.
