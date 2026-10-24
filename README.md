# Automain

Automain imports GitHub issues, pull requests, and discussions, finds unlinked
relationships across repository activity, and groups related items into
persistent threads backed by source evidence.

## Status

Core domain contracts (`internal/domain`), runtime configuration
(`internal/config`), SQLite persistence (`internal/storage/sqlite`), GitHub
ingestion (`internal/github`), candidate discovery (`llm`, `fts`, and `vector`)
and relationship analysis (`internal/analysis`), application service facade
(`internal/app`), HTTP API server (`internal/server`), embedded Svelte UI
(`web`), and the `automain` CLI (`cmd/automain`, `internal/cli`) for `status`,
`version`, `sync`, `items`, `analyze`, `threads`, `relationships`, `stats`,
`graph`, `repositories`, and `serve` are implemented.

## Stack

- **Backend and CLI:** Go
- **Storage:** SQLite
- **Web UI:** Svelte (embedded into the Go binary)

## Build and Run

Prerequisites: Go 1.24+, [Task](https://taskfile.dev), and Node.js 20+ (only when rebuilding `web/dist`).

```sh
# Copy env.example to .env and configure GITHUB_TOKEN / AUTOMAIN_LLM_API_KEY
cp env.example .env

# Rebuild embedded frontend assets (optional when editing web/src/*)
task web

# Build bin/automain
task build

# Run static guard, formatting, vet, and compilation checks
task check

# Run in a disposable /tmp sandbox
task sandbox -- status --json
```

### CLI Commands

```sh
automain status [--db <path>] [--repo <owner/name>] [--json]
automain version [--json]
automain sync --repo <owner/name> [--limit <n>] [--since <RFC3339>] [--full] [--json]
automain items [list|show] --repo <owner/name> [--kind <kind>] [--state <state>] [--dirty] [--number <n>] [--json]
automain analyze --repo <owner/name> [--item <number>] [--limit <n>] [--reanalyze] [--finder llm|fts|vector] [--json]
automain threads [list|show] [--repo <owner/name>] [--id <thread-id>] [--json]
automain relationships [list|show|verify] [--repo <owner/name>] [--id <rel-id>] [--json]
automain stats --repo <owner/name> [--json]
automain graph --repo <owner/name> [--json]
automain repositories [list] [--db <path>] [--json]
automain serve [--listen <host:port>] [--db <path>] [--repo <owner/name>] [--json]
```

### HTTP API (`automain serve`)

- `GET /` serves the embedded Svelte single-page application.
- `GET /healthz`, `GET /api/health`, `GET /api/version`, `GET /api/status`
- `GET /api/repositories`, `GET /api/stats?repo=<owner/name>`, `GET /api/graph?repo=<owner/name>`
- `POST /api/sync` (`repository`, `limit`, `since`, `full`)
- `POST /api/analyze` (`repository`, `item_number`, `limit`, `reanalyze`, `finder`)
- `GET /api/items?repo=<owner/name>&kind=<kind>&state=<state>&dirty=<bool>&limit=<n>`
- `GET /api/items/{number}?repo=<owner/name>`
- `GET /api/threads?repo=<owner/name>&status=<active|resolved|stale>`
- `GET /api/threads/show?id=<thread-id>`, `GET /api/threads/{id}`
- `GET /api/relationships?repo=<owner/name>&type=<type>&verified=<bool>&thread=<id>&item=<n>`
- `GET /api/relationships/show?id=<rel-id>`, `GET /api/relationships/{id}`
- `POST /api/relationships/verify` (`id`, `verified`), `POST /api/relationships/{id}/verify`

## Core Concepts

- **Item:** A GitHub issue, pull request, or discussion, including its comments
  and updates.
- **Thread:** A persistent representation of a shared problem, request, or
  decision containing one or more items. One item can belong to multiple
  threads.
- **Relationship:** A typed link between an item and a thread with a plain-text
  explanation and references to source items or comments.
- **Graph:** The network of threads, items, and relationships stored in SQLite.

## License

[MIT](LICENSE)
