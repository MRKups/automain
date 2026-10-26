# Implementation map

[Project overview](../README.md)

Local `docs/architecture.md` owns product direction and
accepted boundaries. This guide maps the current implementation and lifecycle;
the [active plan](../QDRANT_IMPLEMENTATION_PLAN.md) owns milestone acceptance and
verification evidence. Qdrant/catalog fixtures and container/recovery checks passed
without paid model calls; no live migration or semantic-quality evaluation is claimed.

## Data flow

```text
GitHub REST / GraphQL
    -> github adapter
    -> source SQLite database
         -> titles, bodies, comments
         -> retrieval: exact source passages -> metadata-only SQLite catalog
         -> embedding: Gemini Embedding 2 through OpenRouter -> Qdrant vectors
         -> text search: query embedding -> Qdrant -> current source passages
         -> similar: stored Qdrant vectors -> grouped current item candidates

preserved legacy vectors.db (read-only)
    -> explicit migration -> catalog receipts + compatible Qdrant vectors

explicit state + typed questions
    -> Jev decision adapter through OpenRouter
    -> typed answers, probabilities, model revision, usage (no writes)

source SQLite database
    -> existing analysis: candidate threads + LLM decisions
    -> threads, relationships, evidence records in source database
```

These are independent paths. `index`, `search`, and `similar` do not call `analyze`
or `decide`.
`analyze` does not consume the new embedding index. The [Jev adapter](decision/README.md)
is independently wired through `decide`, without automatic search composition or
relationship policy. The intended new path uses vectors for retrieval, Jev for
judgments, Go for policy, and LLMs only when writing is needed. The old LLM analyser
remains available explicitly through legacy `analyze`; it is not a Jev fallback.

## Ownership

| Module | Responsibility | Does not own |
| --- | --- | --- |
| [domain](domain/README.md) | Data types, validation, effect-port interfaces | HTTP clients or SQL |
| [github](github/README.md) | Fetching and normalizing repository activity | Analysis or vectors |
| [storage/sqlite](storage/sqlite/README.md) | Canonical items, comments, threads, relationships | Embedding generation |
| [retrieval](retrieval/README.md) | Chunking, indexing/reconciliation, migration, text/stored-vector search, freshness checks | Relationship verdicts |
| [embedding](embedding/README.md) | Text-to-vector requests through OpenRouter | SQL or generated explanations |
| [decision](decision/README.md) | Jev typed judgments through OpenRouter | Relationship policy, SQL, retrieval, or writing |
| [storage/catalog](storage/catalog/README.md) | Metadata-only passages, durable indexing/deletion receipts | Vector blobs or canonical source authority |
| [storage/qdrant](storage/qdrant/README.md) | Production `VectorStore` using the official gRPC client | Model calls or relationship decisions |
| [storage/vector](storage/vector/README.md) | Read-only legacy SQLite migration input | Active vector persistence/search |
| [analysis](analysis/README.md) | Existing thread candidate selection and LLM classification | New vector-index search |
| [app](app/README.md) | Shared operation orchestration | Transport-specific presentation |
| [cli](cli/README.md), [server](server/README.md) | User-facing transports | Separate business rules |
| [config](config/README.md) | Settings, defaults, validation | Performing model requests |

## Entry points and lifecycle

[`cmd/automain/main.go`](../cmd/automain/main.go) resolves configuration and
provides the service factory used by the CLI. The factory opens canonical SQLite
and the separate catalog, constructs a lazy Qdrant client, and wires GitHub,
legacy analysis, OpenRouter embeddings, and Jev decisions. It closes each owned
resource on exit. Construction, status, preparation, sync, and standalone decisions
do not contact Qdrant. Preparation and status make no model requests.

Retrieval accepts independent catalog and vector ports. The catalog tracks pending
writes before Qdrant upserts and records completion only after remote acknowledgement
and canonical freshness checks. Durable tombstones survive interrupted cleanup.
The old vector package is opened only by explicit migration.

`serve` exposes the same application service to HTTP and serves the embedded
[web UI](../web/README.md). Operations are explicit calls, not scheduled jobs.
There is no automatic sync/index/search/decision loop.

Source SQLite, catalog SQLite, and Qdrant have no shared transaction. Retrieval
checks source passages against canonical data before returning them. `index
reconcile` repairs missing-point receipts and resumes deletion without models.
Readiness probes the two local stores and Qdrant, unlike status's wiring flags.
See [storage operations](storage/README.md) and [deployment](../deployment/README.md).

## Important distinctions

- A source passage is original text. A relationship explanation is model output.
- A cosine score is similarity, not probability or proof of a particular link.
- Index freshness is separate from an item's legacy analysis dirty flag.
- Source import limits, embedding work limits, and search result limits use
  different units. See the [CLI guide](cli/README.md#limits-and-work-units).
- The new embedding path splits long text. The legacy analyser instead truncates
  prompt inputs. These behaviors must not be described as interchangeable.

For checks and their limits, see [development checks](../scripts/README.md).
