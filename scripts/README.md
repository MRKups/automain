# Development checks

[Project overview](../README.md) | [Taskfile](../Taskfile.yml)

## Static checks

```sh
task check
git diff --check
```

`task check` runs the source guard, verifies Go formatting, runs `go vet ./...`,
and compiles all Go packages. It is not a unit/integration-test suite and does
not rebuild the Svelte UI. Use `task web` followed by `task build` for UI changes.
`task fmt` rewrites Go formatting when needed.

[`check-guard.sh`](check-guard.sh) enforces repository-specific rules: no staged
ignored docs/agent instruction files, no permanent `*_test.go` files, no Go
`unsafe`, `panic`, or linter-suppression escape hatches, and no em/en dashes in
checked source/prose. Restored local contracts in the ignored `/docs/` directory
and focused guides beside code are linked through the
[documentation index](../docs/README.md); ignored documentation must not be staged.

## Retrieval checks with disposable Qdrant

```sh
task check:retrieval
GOFLAGS=-race task check:retrieval
```

[`check-retrieval.py`](check-retrieval.py) requires Python 3, Go, Docker, and a
running Docker daemon. It builds a temporary binary, creates disposable source
and metadata-catalog files, starts a loopback mock embeddings API, and launches
an authenticated `qdrant/qdrant:v1.20.0` container. Docker may download the pinned
image. Both Qdrant ports use dynamically allocated loopback bindings, with no
host or persistent volume mounts. An unavailable daemon fails the check, rather
than skipping it. Cleanup removes only the fixture container and scratch files.

Inherited application/provider settings are cleared for child processes, so
checks do not use real keys or configured databases. Each fixture uses an isolated
collection prefix. SQLite vector files are migration inputs only, never the active
index.

Coverage includes metadata-only catalog ownership, UTF-8 passage offsets, offline
preparation, exact Qdrant/cosine parity, repository and dimension isolation,
bounded indexing and unchanged cache reuse, stored-passage/item similarity without
provider keys, grouped source evidence, source freshness, remote point loss and
restoration, dropped-collection errors and restoration, tombstone cleanup, and
resumable schema-v1 migration without embedding calls. CLI/HTTP wiring and
health/readiness endpoints are exercised. Strict
OpenRouter checks retain response reordering, malformed vector/model responses,
oversized inputs, safe errors, and no automatic retries/redirects. Both env
examples are loaded through the real dotenv loader.

**No live GitHub or OpenRouter calls are made.** Fixture vectors are controlled
test data, not semantic embeddings. Passing checks does not establish live model
availability, semantic retrieval quality, relationship accuracy, large-index
performance, complete ingestion, or browser correctness. The `-race` variant
instruments the Go binary; it does not prove absence of cross-process races.

## Offline Jev checks

```sh
task check:decisions
GOFLAGS=-race task check:decisions
```

[`check-decisions.py`](check-decisions.py) builds a temporary binary and uses a
loopback mock Decisions API with disposable databases and synthetic keys. It
checks all three primitives, structured inputs, numeric presence/ranges,
distributions/legends, returned model identity, usage/cost, credential precedence,
CLI and HTTP routes, invalid inputs/configuration, provider errors, timeouts,
size/depth limits, no retries/redirects, and absence of source/catalog writes.
Qdrant is deliberately unavailable during this check; no Docker daemon or
Qdrant credentials are required. Liveness succeeds while readiness reports the
missing dependency. The retrieval check also loads both env templates to verify
Jev defaults.

No live or paid model calls are made. These checks do not establish Jev accuracy,
calibration, live availability, latency, prices, or a correct relationship policy.

## Isolated runs

```sh
task dev -- status --json
task sandbox -- status --json
```

- `dev` uses persistent source/catalog files under `~/.automain-dev`.
- `sandbox` uses disposable files under `/tmp/automain-sandbox.*`, removed when
  the task exits. It keeps both SQLite paths isolated together.

These tasks isolate database paths, **not credentials or network access**. They
still inherit environment settings and load the current directory's `.env`.
Running sync, embedding, search, decisions, or analysis there can contact live
providers and incur costs. Qdrant collections also need an isolated prefix when
using a shared server. Use `check:retrieval` and `check:decisions` for synthetic
fixtures without live model calls.
