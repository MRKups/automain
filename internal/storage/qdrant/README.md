# Qdrant vector storage

This adapter implements [`domain.VectorStore`](../../domain/retrieval.go) with
the official `github.com/qdrant/go-client/qdrant` v1.20.0 client over gRPC,
targeting Qdrant 1.20.0. It stores derived vectors and full passage payloads,
not canonical items, threads, or relationships. It makes no model calls.

`New` accepts a host, gRPC port, TLS flag, API key, and collection prefix. Empty
host and zero port select `localhost:6334`. Construction does not contact the
server. The composition root owns `Close`. API keys without TLS travel in
plaintext; use that configuration only on trusted local/private networks.

## Storage and queries

Collections are named by the exact prefix followed by the embedding-space ID.
Only a nonempty save creates collections or payload indexes. Each collection has
one unnamed float32 dense vector with the space's exact dimensions and cosine
distance. Incompatible dimensions, distance, named/multiple vectors, sparse
vectors, and quantization are refused rather than changed in place. Existing
default datatype is accepted because Qdrant's default dense datatype is float32.

Points use `domain.PointID`, a deterministic UUID for the repository, passage ID,
and content version. Payloads retain every `SearchChunk` field plus `space_id`
and `item_key`. The grouping key includes repository, item kind, and number.
The indexed payload fields are `repository`, `item_key`, and `item_kind`.
Collection metadata is reread on operations, so existing indexes are not recreated
on every save and a dropped collection can be rebuilt by the next save.

Writes and deletions use `wait=true` and require a completed operation result.
Deletion combines point IDs with repository and space filters. Missing collections
mean no remote points for presence checks and deletion, allowing explicit
reconciliation to repair catalog receipts. Text-vector search fails with
`external_unavailable` if its collection is missing, including a drop during a
query. Run `index reconcile`, then restore or migrate saved vectors, or run
`index embed`, before retrying search. Authentication failures, timeouts, and
other server failures remain errors.

Presence checks fetch full payloads and vectors. A passage is reported present only
when its unnamed dense vector has the space's exact dimensions, finite values,
and nonzero norm, its stable identity, content hash, text, and embedding inputs
match the supplied passage, and its point ID, repository, item identity, grouping
key, and space are valid. Author and URL are mutable metadata, not embedding
inputs; changes to them do not invalidate a cached vector.

A surviving payload without its dense vector is missing, not reusable.
Reconciliation marks it pending; migration can restore a saved compatible vector
without inference, or `index embed` can regenerate it at provider cost. Malformed
remote payloads and nonempty invalid vectors fail explicitly instead of being
reused or silently regenerated. Catalog counts alone do not prove remote vector
completeness.

Search uses exact, unquantized cosine queries with repository and space filters.
Results contain validated passage payloads, not vectors, and are sorted by
descending finite score, then passage ID. Stored-passage search validates the
query point's stable identity, embedding inputs, and actual unnamed dense vector
before querying by ID, excludes its whole source item, and needs no embedding
request. Missing or stale query passages, including payload-only points, return
`not_found`. The application service must still check canonical source
freshness: this adapter cannot make SQLite and Qdrant reads atomic.

## Limits and failures

- Search accepts 1-100 results.
- Writes, presence checks, and deletion accept at most 256 passages/points.
- Each passage's string fields total at most 64 KiB and must be valid UTF-8.
- A write batch accepts at most 8 MiB of estimated payload and vector data;
  presence checks accept at most 8 MiB of estimated supplied payload data. Both
  include a per-point serialization allowance. Oversized inputs fail before any
  network request. The gRPC receive limit is 16 MiB, allowing presence checks to
  fetch 256 supported 3072-dimensional float32 vectors alongside those payloads.
  Vectors must match the space, be finite, and have nonzero norm.
- Calls preserve the caller's deadline; without one, an operation has a
  30-second timeout. Health checks always have at most five seconds and invoke
  Qdrant's `HealthCheck` RPC, without creating collections or calling a model.

Failures use structured domain errors without server payload dumps or secrets.
After an interrupted write, reconcile remote presence before repeating paid
embedding work. A successful build does not establish server compatibility:
runtime acceptance requires a disposable real Qdrant 1.20.0 container, including
collection compatibility, exact-search parity, scope isolation, drop/recovery,
and interrupted-write/deletion checks. Python local mode is not a substitute.
