# Automain

Automain imports GitHub issues, pull requests, and discussions, finds unlinked
relationships across repository activity, and groups related items into
persistent threads backed by source evidence.

## Status

Core domain contracts (`internal/domain`), runtime configuration
(`internal/config`), SQLite persistence (`internal/storage/sqlite`), application
service facade (`internal/app`), embedded web asset boundary (`web`), and the
`automain` CLI (`cmd/automain`, `internal/cli`) for `status`, `version`,
`threads`, `relationships`, and `stats` are implemented. GitHub ingestion
(`sync`), relationship analysis (`analyze`), HTTP endpoints (`serve`), and the
Svelte UI are not wired yet.

## Stack

- **Backend and CLI:** Go
- **Storage:** SQLite
- **Web UI:** Svelte (embedded into the Go binary)

## Build and Run

Prerequisites: Go 1.24+ and [Task](https://taskfile.dev).

```sh
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
automain analyze --repo <owner/name> [--item <number>] [--limit <n>] [--reanalyze] [--json]
automain threads [list|show] [--repo <owner/name>] [--id <thread-id>] [--json]
automain relationships [list|show|verify] [--repo <owner/name>] [--id <rel-id>] [--json]
automain stats --repo <owner/name> [--json]
automain serve [--listen <host:port>] [--db <path>]
```

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
