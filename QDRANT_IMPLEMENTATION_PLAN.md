# Automain: Qdrant and Containerization Plan

Status: phases 1-5 implemented with passing fixtures, but six subsequent review
findings remain open, October 26, 2026. Acceptance is not complete for the affected
migration, recovery, configuration, and deadline guarantees. The restored
[architecture](docs/architecture.md) owns product direction and boundaries; this
document owns the game plan, migration work, and acceptance checks.

## Review follow-up

The implementation is being preserved with these known defects, not represented
as a release-ready cutover. Existing fixture results below remain historical evidence;
the broader review exercised cases those checks missed. No fixes are included yet.

| ID | Open defect and owning guide | Required verification before closure |
| --- | --- | --- |
| R1 | [Legacy WAL reader](internal/storage/vector/README.md#accepted-files-and-ownership) can fail and create a sidecar after clean shutdown | Clean/stopped WAL inputs migrate without any source or sidecar changes |
| R2 | [Qdrant presence](internal/storage/qdrant/README.md#storage-and-queries) accepts payloads without vectors | Vector-only loss becomes pending and saved vectors restore it without inference |
| R3 | [Compose configuration](deployment/README.md#start-and-inspect) drops supported legacy credentials/endpoints and forces the wrong default model | Host/container provider, credential presence, model, and endpoint resolution agree |
| R4 | [Mutation queue](internal/app/README.md#concurrency-and-partial-progress) ignores cancellation while waiting | Canceled queued operations return before the active provider call finishes |
| R5 | [HTTP body reads](internal/server/README.md#deployment-limits) can outlive operation deadlines | Stalled decision/retrieval bodies terminate within the configured request bound |
| R6 | [Embedding timeouts](internal/embedding/README.md#failures-and-cost) become internal errors | CLI/API classify provider deadlines as external failures, without retries |

## Reconciliation with restored design docs

The restored [documentation index](docs/README.md) links the updated contracts.
The contributor/development policy and canonical evidence requirements remain
in force. `development.md` and `documentation-style.md` were restored unchanged.

- The old single-SQLite deployment becomes canonical SQLite plus a separate
  catalog and Qdrant. This does not move authoritative threads or reviews.
- Legacy `fts`/`vector` thread finders are in-memory lexical/TF-IDF strategies,
  not the new semantic index. Do not wire Qdrant through that old port by name.
- Legacy classification remains explicit. Jev and requested LLM writing are
  separate roles in the intended workflow, not an automatic search pipeline.
- Existing relationship records link items to threads. Mapping item-pair Jev
  judgments to those records, explanations, and claim-bound reviews is unresolved.
- Quote checking, review invalidation, import coverage/deletions, and cross-kind
  identity collisions are existing gaps. Qdrant cannot recover absent source data
  or implicitly fix them. Before migration, inventory collisions and preserve
  available kind/source identity; broader source-model changes need their own slice.
- The old Railway/work-order descriptions are not deployment or verification
  evidence. Compose is the active target; no work orders were included in the bundle.

Keep phases 1-5 below focused on retrieval and deployment, not silent repair or
replacement of the legacy relationship workflow.

## Goal

Ship a simple, persistent Docker Compose stack using **Qdrant 1.20.0** for vector
search. Preserve source-backed results and existing embeddings. Keep decisions
and writing separate from retrieval.

**First milestone:** containerized import, indexing, text search, and similarity
search using stored vectors. No automatic Jev classification on search results.

## Agreed Architecture

| User need | Responsible component |
| --- | --- |
| Find similar activity | Qdrant vector search |
| Search with new text | Gemini query embedding, then Qdrant |
| Find candidates for an already indexed passage | Reuse its stored vector; no new embedding call |
| Follow an explicit GitHub reference | Deterministic code, where imported reference data supports it |
| Decide whether/how selected items relate | Explicit, bounded Jev evaluation |
| Write prose or a summary | A generative LLM, only when writing is requested |

Vectors shortlist candidates across the repository. Jev answers a particular
relationship question when similarity alone is insufficient. **Search must work
without Jev credentials and must not silently invoke Jev or legacy analysis.**

```text
Browser / API client
    -> automain container: Go API + embedded Svelte UI
         -> source SQLite: canonical records, relationships, reviews
         -> separate SQLite catalog: passages and indexing progress, no vectors
         -> qdrant container: vectors, searchable passage payloads
         -> OpenRouter: Gemini embeddings; Jev only on explicit decision calls
```

- Exactly two runtime services: `automain` and `qdrant`.
- Pin `qdrant/qdrant:v1.20.0`; upgrades are deliberate, not a moving `latest` tag.
- Keep `google/gemini-embedding-2`, **3072 dimensions**, cosine similarity.
- Start with **one dense vector per passage**, not separate `link`/`search` copies.
- Keep Jev's existing pinned default, `typesafe/jev-1.14`, through OpenRouter.
- No self-hosted inference, extra database server, worker service, or Kubernetes.

## Current State

- Import, source SQLite, chunking, embedding, search, CLI/API, and embedded UI exist.
- [`ChunkCatalog` and `VectorStore`](internal/domain/retrieval.go) separate catalog
  operations from vector persistence/search. The metadata-only
  [SQLite catalog](internal/storage/catalog/README.md) persists passages, progress,
  and deletion receipts; [Qdrant](internal/storage/qdrant/README.md) stores vectors.
- [`retrieval.Service`](internal/retrieval/service.go) already separates retrieval
  from relationship judgments and checks hits against current source records.
- [`decide`](internal/decision/README.md) already accepts typed state/questions and
  returns probabilities and usage without generating prose or saving relationships.
- Legacy `analyze` remains a separate generative pipeline. It is not the future
  decision path and must never become a fallback for Qdrant or Jev failures.
- The old SQLite vector adapter is now an explicit read-only migration reader.
  Existing vector files are neither opened nor changed during ordinary startup.
- Docker Compose supplies two persistent services, verified with real Qdrant
  1.20.0 and synthetic provider fixtures. No user database has been migrated.

## Storage and Retrieval Design

### Storage contracts

The initial port split and physical separation are implemented and verified.

- **`ChunkCatalog`:** prepare/reconcile passages, track content hashes and embedding
  spaces, identify pending work, and record confirmed remote indexing progress.
  Keep it in a separate metadata-only `index.db`, outside canonical source tables.
  This preserves offline, no-model-call `index prepare`.
- **`VectorStore`:** validate collections, upsert/delete points, query vectors or
  stored point IDs, and inspect remote presence. Implement it with Qdrant's official
  Go client over gRPC. Qdrant becomes the sole production vector backend.

Refactor domain contracts, retrieval orchestration, configuration, status output,
and [`cmd/automain/main.go`](cmd/automain/main.go) together. Do not merely replace
the SQLite `Open` call while retaining assumptions about one atomic database.

### Qdrant collections and points

- One collection per embedding space, using the existing model/dimensions/recipe
  identity. Never mix incompatible embeddings, even when dimensions match.
- One point per **source passage**, not one point per GitHub item. Preserve the
  existing chunking and explicit input-size rejection; never truncate long items.
- Derive deterministic UUIDs from chunk identity and content version. Preserve
  original IDs in payloads; a GitHub node ID alone cannot identify multiple chunks.
- Payloads carry repository, item kind/number, item grouping key, source kind,
  comment ID where applicable, exact text, byte offsets, title context, URL, and
  content hash. Qdrant is a derived index, not the canonical record.
- Create payload indexes for fields actually used in filtering/grouping, initially
  repository, item grouping key, and kind. Require repository scoping on queries.
- Stored-ID lookup must validate repository, embedding space, and current content
  too; candidate filters alone do not validate the query point.
- Keep passage search as-is. Add explicit stored-passage similarity search, then
  item-level candidate grouping over a bounded set of that item's passages.
  Exclude the source item and return supporting passages, not similarity verdicts.
- Validate search against exact, unquantized results first. Document that normal
  approximate search can change rankings; similarity scores are not probabilities.

### Recovery and migration

- SQLite and Qdrant do not share a transaction. Use idempotent point writes and
  record completion only after Qdrant confirms the operation has completed.
- Recheck source hashes around indexing. Old in-flight responses must not mark
  changed content current. Filter stale hits against canonical sources on reads.
- Reconcile changed/deleted passages and old content-version points. Persist enough
  cleanup state to resume after a crash; do not rely on an in-memory deletion list.
- Reconcile missing remote points too. A restored/empty Qdrant volume must not leave
  the catalog reporting everything indexed. Recovery should reuse matching remote
  points after interrupted writes instead of unnecessarily re-embedding them.
- Preserve existing `vectors.db`. Provide an explicit, resumable import of compatible
  vectors and passage metadata into the new catalog/Qdrant without provider calls.
  Do not overwrite the old file, silently discard spaces, or regenerate paid vectors.
- Verify counts, hashes, sample search results, and repeat-run idempotency before
  switching over. Keep original files/backups until migration is accepted.

## Implementation Phases

| Phase | Work | Completion gate | Status |
| --- | --- | --- | --- |
| 1. Storage boundaries | Catalog/vector contracts, IDs, lifecycle, progress, cleanup | Offline preparation; canonical state unchanged | Implemented; R4 open |
| 2. Qdrant adapter | Collections, indexes, upserts/deletion, vector/stored-ID queries | Real Qdrant 1.20 fixtures, no paid model calls | Implemented; R2 open |
| 3. Wiring and migration | Retrieval/config/status, legacy import, item grouping | Vector reuse, restart/recovery, freshness | Implemented; R1, R5, R6 open |
| 4. Containers | Images, Compose, volumes, networking, health | Fresh startup, UI/API, persistence and restore | Implemented; R3 open |
| 5. Operational handoff | Env examples, tasks, guides, migration/backup instructions | Checks and local documentation links pass | Updated with open review findings |
| Later, separate milestone | Explicit Jev relationship policy | Reviewed questions/thresholds before writes or automation | Deferred |

### Implementation progress

The initial interface-only slice has been superseded by physical catalog/Qdrant
separation. Production no longer uses the old paired SQLite vector backend.

Independent verification passed these fixture suites, including the then-known
missing/empty-collection and source-append regression reruns:

```sh
task check
task check:retrieval
env GOFLAGS=-race task check:retrieval
task check:decisions
env GOFLAGS=-race task check:decisions
go mod verify
```

Reported runtime:

```text
go version go1.27.3 darwin/arm64
Docker server: 29.8.2
```

Retrieval checks use real authenticated Qdrant 1.20.0 containers and loopback model
fixtures. Separate catalog/core exercises cover failed acknowledgements, durable
deletions across process exits, stale in-flight appends, pagination, and continued
bounded migration across multiple spaces. No live GitHub/OpenRouter or paid calls
were made. Migration uses controlled schema-v1 fixtures; no user database was migrated.

Cold and final container builds retained `-mod=readonly`. Real Compose checks passed
readiness/liveness, embedded assets, non-root SQLite permissions, restart/down-up
persistence, Qdrant outage behavior, and stopped-volume backup/restore into fresh
volumes. These are operational fixture checks, not semantic-quality or capacity
measurements. Real remote TLS deployment and hardware power-loss timing were not tested.

**Implemented components:**

- Metadata-only catalog in `index.db`, with its own application ID. Pending writes
  are tracked before Qdrant upserts; current source versions are confirmed only
  after completed remote operations and canonical-source checks.
- Changed/deleted tracked versions retain durable deletion receipts. Reconciliation
  removes those points idempotently and marks missing remote points pending again.
- Qdrant uses the official pinned Go client, one collection per embedding space,
  deterministic content-version UUIDs, indexed payload filters, and exact cosine
  search initially. Stored-ID queries require no embedding request.
- `similar` returns grouped item candidates with supporting passages. Item-level
  queries use at most 32 available stored source vectors and explicitly report truncation.
- `index migrate --from` opens old vector data read-only, preserves embedding-space
  identity, reuses matching stored vectors, and reports stale/unavailable-source
  skips. Bounded repeated runs scan past completed writes rather than starving later work.
- CLI/API expose reconciliation, migration, and similarity through shared services.
  Readiness checks local persistence and Qdrant; liveness and standalone Jev remain
  independent of Qdrant availability.
- The app image builds Svelte and Go, runs non-root, and uses persistent Compose
  volumes with loopback app publishing and private Qdrant.

The independent audit's source-append, available-passage selection, and missing
collection findings were corrected and rechecked. Healthy-but-empty indexed
collections also fail clearly, while legitimately stale filtered candidates may
return empty. Config/transport checks confirm invalid inputs fail before effects
and secrets stay redacted. Local links and protected documentation hashes passed;
`development.md` and `documentation-style.md` are unchanged.

### Container and configuration requirements

- Multi-stage build: Node builds Svelte, Go embeds the built assets, then a small
  non-root runtime image serves the app. No separate frontend server in production.
- Named `automain-data` volume for SQLite source/catalog and their journal files;
  named `qdrant-data` volume for Qdrant storage. Document snapshots and consistent
  SQLite backups; a volume is persistence, not a backup.
- App listens on `0.0.0.0:8080` inside its container. Publish only
  `127.0.0.1:8080:8080` by default. Keep Qdrant's ports private to Compose.
- Use one app instance and one mutation owner initially. While serving, perform
  mutations through its API rather than concurrent CLI indexing processes sharing
  the same files. Do not claim that SQLite plus a process-local mutex supports
  horizontal app scaling or distributed indexing.
- Add `.dockerignore`: exclude real env files, credentials, databases, local build
  outputs, and dependency directories; retain source and build manifests.
- Pass runtime secrets to the app, never image build layers or browser assets.
  Configure a Qdrant API key without exposing it in logs/status. Do not pass the
  OpenRouter key to the Qdrant container.
- Implemented settings: `AUTOMAIN_INDEX_DB_PATH`, `AUTOMAIN_QDRANT_HOST`,
  `AUTOMAIN_QDRANT_PORT`, `AUTOMAIN_QDRANT_TLS`, `AUTOMAIN_QDRANT_API_KEY`, and a
  `AUTOMAIN_QDRANT_COLLECTION_PREFIX`. Default local gRPC port is 6334; Compose overrides the host
  to `qdrant`. Plaintext is limited to local/private deployment; remote deployments
  require TLS. Names are consistent across code, examples, and guides.
- Replace active vector-file configuration with catalog/Qdrant settings explicitly.
  Document the old `--vector-db`/`AUTOMAIN_VECTOR_DB_PATH` migration rather than
  silently reinterpreting an old vector database as a new metadata catalog.
- Keep existing embedding/Jev defaults prefilled in `.env.example` and `env.example`;
  real keys stay blank. Prefer explicit Compose overrides for container paths/hosts.
- Keep liveness separate from readiness. Readiness checks Qdrant/local storage with
  bounded requests and no model calls. Use health-check commands actually present
  in the images. Handle Qdrant outages after startup, not just startup ordering.
- Do not make sync, offline preparation, standalone `decide`, or local configuration
  inspection depend on Qdrant availability. Vector operations fail clearly without
  hidden fallback; diagnostics report unavailable components without paid probes.

## Acceptance Checks

- [x] Container image builds the frontend and binary from source without embedding secrets.
- [x] Fresh Compose startup, readiness, shutdown, and volume-backed restart succeed.
- [x] Source SQLite contains no vectors; the catalog contains no vector blobs.
- [x] Preparation works with Qdrant stopped and no provider keys.
- [x] Indexing batches resume; unchanged content is reused; bad vectors are rejected.
- [ ] Existing compatible embeddings migrate with **zero embedding API calls**, including clean WAL-mode inputs (R1).
- [x] Text search calls only the query embedding provider, never Jev or a chat model.
- [x] Stored-passage similarity calls **no embedding, Jev, or chat provider**.
- [x] Grouped item candidates exclude the source item and retain exact source evidence.
- [x] Repository/kind/space boundaries, UUID mapping, and collection compatibility hold.
- [x] Changed/deleted sources and stale in-flight writes cannot produce current-looking hits.
- [ ] Interrupted writes/deletions and missing/restored Qdrant vectors reconcile correctly, including payload-only points (R2).
- [x] Qdrant outages yield safe errors; standalone Jev and non-vector operations still work.
- [x] Ordinary restart or search does not create relationships or trigger legacy `analyze`.
- [x] Static checks, existing decision checks, and updated retrieval checks pass.

Use a real disposable **Qdrant 1.20.0 container** for adapter/integration checks,
not Python local mode. Mock OpenRouter for all automated fixtures. A live paid
evaluation is a separate, explicitly approved activity, not part of container tests.
Start with exact-search parity and measure approximate recall before tuning it.

## Deferred Work

- **Jev policy:** later introduce an explicit read-only relationship check over
  selected candidates, not every search hit or every possible item pair. Test
  atomic questions for shared problem, duplicate report, proposed fix, implemented
  decision, and dependency in each direction. Allow multiple relationships and
  distinguish unrelated from insufficient evidence.
- **Evaluation before automation:** build a small labeled set of clear matches,
  misleading similarities, unrelated pairs, and uncertain cases. Preserve all
  returned probabilities; choose thresholds based on evidence and error costs.
- **Relationship persistence:** only after policy evaluation; tie judgments/reviews
  to source snapshots, model version, and question revision. No generated quotes.
- **Writing:** a separate optional LLM step using selected source evidence. The
  current legacy analyser is not a writing-only service; do not reuse it as one.
- **Hybrid search:** add only for measured retrieval gaps. Qdrant 1.20's native
  BM25/full-text search can complement dense retrieval without a sparse-model
  service. Keep exact identifier/reference handling in code where appropriate.
- **Extra vectors/compression:** no second dense representation or quantization
  initially. Benchmark a concrete benefit before adopting either.
- **UI expansion and scaling:** no new relationship/search UI, background scheduler,
  multi-node cluster, or distributed app writers in the first milestone.

Capacity planning must count **passages**, not GitHub items. One million
3072-dimensional float32 vectors occupy about **12.3 GB of raw vector data** before
indexes, payloads, replication, and headroom. RAM depends on placement and workload;
quantization does not automatically reduce total RAM by 75%. Measure a representative
sample before sizing hardware; do not infer a growth deadline from an assumed rate.

## Operating the Finished Stack

Follow the [container guide](deployment/README.md) to create a local `.env` without
overwriting an existing one and set `AUTOMAIN_QDRANT_API_KEY`. Start the stack with:

```sh
docker compose up --build -d
```

Then use the app's API for import/index mutations and search. Repeat bounded
embedding batches to finish pending work. Use explicit decision requests only
when a typed judgment is needed; ordinary search remains independent.

Document shutdown, logs, backup/restore, migration, and non-destructive recovery.
Never recommend `docker compose down -v` as routine shutdown: it removes data volumes.

**Next milestone:** decide how to apply Jev to specific relationship questions.
First close R1-R6 and repeat the affected verification. Do not infer authorization
for deferred automation, writing, UI, or scaling work from the implementation status.

## References

- [Existing retrieval contract](internal/domain/retrieval.go) and [retrieval guide](internal/retrieval/README.md)
- [Existing Jev plumbing and studied sources](internal/decision/README.md)
- [Qdrant 1.20.0 release](https://github.com/qdrant/qdrant/releases/tag/v1.20.0)
- [Query-by-ID and item grouping](https://qdrant.tech/documentation/search/search/)
- [Named vectors](https://qdrant.tech/documentation/manage-data/vectors/)
- [Capacity planning](https://qdrant.tech/documentation/guides/capacity-planning/)
- [Quantization tradeoffs](https://qdrant.tech/documentation/manage-data/quantization/)
