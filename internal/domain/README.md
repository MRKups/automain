# Domain types and contracts

[Architecture](../README.md)

## Source records and decisions

- **Item:** an imported GitHub issue, PR, or discussion. Current storage identifies
  it by repository and number. It includes title, body, state, author, labels,
  source URL, timestamps, content hash, and last analysed hash.
- **Comment:** imported text attached to an item, identified by repository and
  comment ID. Discussion replies use this same representation.
- **Thread:** an Automain grouping around a shared problem or decision, not a
  GitHub discussion thread. One item may belong to multiple Automain threads.
- **Relationship:** a typed item-to-thread link with an explanation, confidence,
  evidence references, explicit-link flag, and human-verification flag.
- **EvidenceRef:** an item-body or comment citation. Domain validation checks
  required fields and types; it does not prove the quote occurs in source text.

Relationship types are `reports_same_problem`, `proposes_fix`,
`implements_decision`, `blocks_or_depends`, `related_context`, and `duplicate_of`.
Confidence is `high`, `medium`, or `low`. Thread status is `active`, `resolved`, or
`stale`. These are stored values, not guarantees of model correctness.

## Retrieval records

[`retrieval.go`](retrieval.go) defines the separate search contracts:

- **SearchChunk:** a source passage with stable ID, repository/item identity,
  source kind, optional comment ID, title context, exact text, byte offsets,
  source author/URL, and content fingerprint.
- **EmbeddingSpace:** provider, model, revision, and dimension count. Its ID
  separates vectors that must not be compared or reused together.
- **ChunkEmbedding:** a vector paired with the full source passage and its input
  fingerprint, so storage receives point payloads without a catalog lookup.
- **SearchMatch:** a chunk and cosine similarity. It is not a relationship verdict.
- **IndexStatus:** prepared passage count and embedded/pending counts by space.
- **SimilarResult:** grouped item candidates with supporting passages, the number
  of source passages queried, and an explicit bounded-selection flag.

Search source kinds include `item_title`, `item_body`, and `comment`. Title
passages keep empty-body items searchable. Offsets are UTF-8 byte offsets, not
character positions or line numbers.

## Effect ports

| Port | Boundary |
| --- | --- |
| `Store` | Canonical source/relationship persistence |
| `GitHubSource` | Source ingestion |
| `CandidateFinder` | Legacy selection of candidate threads |
| `RelationshipAnalyser` | Legacy item-to-thread decision output |
| `EmbeddingProvider` | Text-to-vector generation, with query/document purposes |
| `DecisionProvider` | Typed state/questions to Jev answers and probabilities; no writing or storage |
| `ChunkCatalog` | Derived passages, embedding spaces, pending/indexed receipts, durable deletion tombstones, and index status |
| `VectorStore` | Repository/space-scoped vector writes, presence checks, deletion, text-vector and stored-passage queries |
| `LegacyIndex` | Read-only access to preserved SQLite passages/vectors for explicit migration |

The production catalog uses separate metadata-only SQLite; Qdrant implements the
vector port. Catalog tracking precedes remote writes, and confirmation follows
remote completion and canonical freshness checks. Changed/removed tracked
versions become durable tombstones. Deterministic point UUIDs include repository,
passage ID, and content version; spaces use separate collections. These stores
do not share a transaction. The composition root owns and closes their resources.

Embedding providers must return one finite, nonzero vector per input, in input
order, at the declared dimensionality. Storage and service boundaries validate
these conditions. Revision must change with model-version or task/input-recipe
changes that would make cached vectors incompatible.

[`decisions.go`](decisions.go) defines `DecisionRequest`, typed questions/answers,
usage, and JSON/input validation. Raw JSON preserves structured state, instructions,
and criteria. Nullable numeric pointers distinguish missing fields from real zero
values. These answers are not legacy `AnalysisDecision` records; probabilities
must not be silently mapped to categorical relationship confidence or persisted.
See [Jev decisions](../decision/README.md) for semantics and response checks.

See [retrieval](../retrieval/README.md) for exact-source validation and
[analysis](../analysis/README.md) for the different legacy evidence behavior.
