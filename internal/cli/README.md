# CLI

[Project overview](../../README.md) | [Configuration](../config/README.md)

Build with `task build` and run `./bin/automain`, or use
`go run ./cmd/automain`. Examples below assume `automain` is on your PATH.
`automain --help` and `automain <command> --help` print usage.

## Global flags

Global flags can appear before or after the command:

| Flag | Purpose |
| --- | --- |
| `--db <path>` | Canonical database |
| `--index-db <path>` | Metadata-only passage catalog |
| `--vector-db <path>` | Deprecated legacy read-only source path, never the catalog; migration still requires `--from` |
| `--repo owner/name` | Repository, overriding configured default |
| `--json` | Structured output |
| `--timeout 5m` | Operation timeout; default 60s |
| `--finder llm\|fts\|vector` | Legacy analysis candidate strategy |
| `--listen host:port` | HTTP binding; default loopback port 8080 |

`serve` runs until interrupted rather than stopping after `--timeout`; the setting
controls API request deadlines. Its HTTP write timeout remains fixed at 120s.

## Commands

| Command | Purpose and main options |
| --- | --- |
| `status`, `version` | Local configuration/adapters or version |
| `sync` | Import: `--repo`, `--limit`, `--since <RFC3339>`, `--full` |
| `items [list]` | Inspect: `--repo`, `--kind`, `--state`, `--dirty`, `--limit` |
| `items show` | Item/comments: `--repo`, `--number` (`--item` alias) |
| `index [status]` | Prepared/vector counts: `--repo` |
| `index prepare` | Reconcile complete local source snapshot: `--repo`; no limit |
| `index embed` | Prepare and embed pending passages: `--repo`, `--limit` |
| `index reconcile` | Check missing remote points and resume cleanup: `--repo`; no model calls |
| `index migrate` | Import compatible legacy vectors read-only: `--repo`, explicit `--from <path>`, `--limit`; no model calls |
| `search` | Passage retrieval: `--repo`, `--query`, `--limit` |
| `similar` | Grouped candidate items from stored vectors: `--repo`, exactly one `--chunk <id>` or `--item <number>`, `--limit`; no model calls |
| `decide` | Jev typed questions over supplied state: `--input <request.json>`; no repo required |
| `analyze` | Legacy LLM decisions: `--repo`, `--item`, `--limit`, `--reanalyze` |
| `threads [list]` | Thread list: `--repo`, `--status active\|resolved\|stale` |
| `threads show` | Thread detail: `--id` |
| `relationships [list]` | Links: `--repo`, `--thread`, `--item`, `--type`, `--verified true\|false` |
| `relationships show` | Link/evidence detail: `--id` |
| `relationships verify` | Mark reviewed: `--id`; `--unverify` clears the flag |
| `stats`, `graph` | Repository summaries: `--repo` |
| `repositories [list]` | Imported repository metadata; `repos` is an alias |
| `serve` | HTTP API and embedded UI |

Listing is the default action for items, threads, relationships, and repositories;
index defaults to status. Flags for an action should follow that action.

## Limits and work units

| Command | Unit | Default / valid range |
| --- | --- | --- |
| `sync --limit` | GitHub items, not comments | 0 = no cap; nonnegative |
| `items list --limit` | Returned items | 0 = no cap; nonnegative |
| `analyze --limit` | Eligible items, not model requests | 0 = no cap; nonnegative |
| `index embed --limit` | Pending passages | 100; 1-10000 |
| `index migrate --limit` | Legacy vector writes | 100; 1-10000 |
| `search --limit` | Matching passages, not distinct items | 10; 1-100 |
| `similar --limit` | Candidate items with supporting passages | 10; 1-100 |
| `decide --input` | UTF-8 JSON bytes | 30000 including model; rejected rather than truncated |

One long item may produce many passages. Preparation always covers the complete
locally stored repository even when embedding is limited. Repeat embedding runs
to finish pending work. Ordinary limited syncs do not paginate through a backlog;
see [import semantics](../github/README.md#pagination-limits-and-cutoffs).

`analyze --finder vector` is legacy TF-IDF, **not** semantic search. See
[analysis](../analysis/README.md) before using its model output or review flags.

`decide` accepts only `state` and `questions` in its file. It returns raw typed
results as JSON, or the usual envelope with `--json`. It does not run search,
legacy analysis, writing, or relationship persistence. See the
[Jev example and contract](../decision/README.md#try-the-plumbing).

## Output and exit status

JSON success uses `{"ok":true,"command":"...","data":...}`. Failures use
`{"ok":false,"command":"...","error":{...}}`, with `code`, `operation`,
`message`, and optional `recovery`. For example, the index command identifier is
`index.embed`. Error summaries also go to stderr, including in JSON mode.
Help is plain text even with `--json`; `serve` emits startup information and
then remains running.

- **0:** success.
- **1:** runtime, external-service, not-found, or internal failure.
- **2:** invalid input/usage.
- **3:** missing configuration such as an embedding key.

Stored-vector similarity excludes the source item and returns supporting passages,
not relationship judgments. A truncated result reports that only a bounded subset
of query passages was used. Neither similarity nor migration needs an embedding key.

Status and preparation make no model calls. Production uses source SQLite and a
separate metadata catalog, never the legacy vector file. Use dedicated paths when experimenting. See
[development sandboxes](../../scripts/README.md#isolated-runs).

Status now includes separate `chunk_catalog` and `vector_store` adapter flags.
The older `vector_index` flag remains true when both are wired. Local receipts
describe confirmed progress, not a live remote presence check; use `index reconcile`
after restoring or replacing Qdrant storage.
