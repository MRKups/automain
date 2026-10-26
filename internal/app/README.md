# Application service

[Architecture](../README.md)

`Service` is the shared entry point for CLI and HTTP operations. It resolves a
repository from explicit input or configured defaults, validates operation
options, calls adapters, and returns domain results. Transport formatting belongs
in [CLI](../cli/README.md) or [server](../server/README.md), not here.

## Wiring

[`cmd/automain/main.go`](../../cmd/automain/main.go) constructs the service with:

- Canonical SQLite `Store`, metadata-only SQLite `ChunkCatalog`, and Qdrant
  `VectorStore` using the official gRPC client.
- GitHub source adapter.
- Legacy candidate finder and relationship analyser.
- OpenRouter Gemini embedding provider.
- OpenRouter Jev decision provider, separate from legacy analysis.
- Read-only legacy vector opener for explicit migration.
- Embedded UI availability.

`NewService` passes the store's read methods, catalog, vector store, and embedder to the
[retrieval service](../retrieval/README.md). Retrieval cannot use that interface
to write canonical records or decisions. Qdrant client construction is lazy and
makes no network request. The factory opens source/catalog files, not legacy vectors.

Status reports configuration without key values, adapter presence, schema
versions, and configured embedding/Qdrant settings. Adapter presence means
an implementation is wired, not that its credentials or remote service work.
Status includes `chunk_catalog` and `vector_store` separately, and
`index_schema_version` identifies the catalog schema. For compatibility,
`vector_index` reports whether both ports are wired, and `vector_schema_version`
also reports the catalog schema, not a Qdrant or legacy-file version. `/readyz`
instead makes bounded source/catalog/Qdrant probes without model calls. Its
success does not establish index completeness or valid provider credentials.

## Operation boundaries

| Operation | Effects |
| --- | --- |
| `Sync` | GitHub reads, transactional source batch upsert |
| `PrepareIndex` | Canonical reads and local catalog reconciliation; no network/model call |
| `EmbedIndex` | Prepare/register space, reconcile remote presence/cleanup, reuse present vectors, embed missing work, confirm completed current batches |
| `ReconcileIndex` | Prepare, resume remote deletion and mark missing points pending; no model call |
| `MigrateIndex` | Explicit read-only legacy input, compatible vectors into catalog/Qdrant; no model call |
| `Search` | Query embedding when indexed, vector search, source freshness checks |
| `Similar` | Current stored-vector queries, grouped item candidates; no model call or provider key |
| `Decide` | Bounded supplied state/questions to Jev; typed answers/usage, no storage or LLM call |
| `Analyze` | Legacy candidate selection/LLM, per-item canonical analysis saves |
| `VerifyRelationship` | Explicit source-database review-flag update |

Sync does not trigger embedding or analysis. Embedding/search does not trigger
relationship decisions. Sync, status, preparation, and standalone decisions do
not contact Qdrant. There is no scheduler, background queue, or downstream
decision policy attached to retrieval. The standalone [Jev boundary](../decision/README.md)
is usable through CLI/API; its automatic composition with retrieval is not chosen.

## Concurrency and partial progress

Sync, legacy analysis, index preparation/embedding, reconciliation/migration, and
verification are serialized per service, including their remote requests. A queued
operation can leave promptly on cancellation or deadline without starting downstream
work. It returns `external_unavailable` with the cancellation cause preserved and
requires an explicit retry. Cancellation is checked again when ownership is granted.
Search, similarity, status/read operations, and standalone decisions do not wait
for mutation ownership.

This protects operations within one service instance, not independent CLI
processes. Separate processes are not coordinated by job locks and may duplicate
requests. Use one mutation owner; while serving, submit mutations through its API
rather than independent CLI writers. Source, catalog, and Qdrant updates have no
shared transaction.

Sync saves one source batch; analysis saves one item at a time; embedding saves
one batch at a time. A failed multi-step operation may therefore leave valid
earlier progress. See [storage](../storage/README.md) and
[retrieval](../retrieval/README.md) for freshness and retry behavior.
