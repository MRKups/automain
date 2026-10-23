# Automain

Automain imports GitHub issues, pull requests, and discussions, finds unlinked
relationships across repository activity, and groups related items into
persistent threads backed by source evidence.

## Status

Pre-implementation scaffold. Only repository configuration is committed on
`main`. Core domain packages, SQLite persistence, GitHub ingestion, relationship
analysis, CLI commands, HTTP endpoints, and the embedded Svelte interface are
not implemented yet.

## Stack

- **Backend and CLI:** Go
- **Storage:** SQLite
- **Web UI:** Svelte (embedded into the Go binary)

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
