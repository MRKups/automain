# Canonical SQLite store

[Storage operations](../README.md) | [Domain contracts](../../domain/README.md)

This adapter implements `domain.Store`. It owns the original imported records and
stored analysis results, not the embedding index. Opening creates parent
directories, enables WAL and foreign keys with a 5-second busy timeout, and runs
forward migrations. Each store uses a single pooled connection.

## Schema

[`migrations.go`](migrations.go) defines schema version 1:

- `repositories`: sync timestamps, cursor/status metadata.
- `items`: GitHub source records keyed by `(repo, number)`, including content and
  last-analysed hashes. Kind is a field, not part of the primary key.
- `comments`: source comments keyed by `(repo, comment_id)`, attached to items.
- `threads`: Automain thread descriptions, status, and revision.
- `relationships`: typed item-to-thread links, explanations, confidence, explicit
  link detection, and human-verification state.
- `relationship_evidence`: source citation fields associated with relationships.
- `schema_migrations`: applied source schema versions.

The source store predates application IDs and expects ID 0. It rejects nonzero
IDs, including the separate vector index's identity. It is not a generic
ownership validator for every SQLite file with ID 0.

## Imports and dirty tracking

`UpsertItemsBatch` saves an imported batch and sync metadata transactionally.
Item hashes track source content and comments; differing content/last-analysed
hashes identify legacy analysis work. Upserts retain the prior analysed hash.
Imported text is not shortened by the embedding chunker or legacy prompt builder.

Imports upsert returned comments; they do not delete locally stored comments
absent from an upstream response. Deleted upstream text can therefore remain in
local storage and in subsequently prepared search passages. See
[GitHub coverage limits](../../github/README.md#current-limits).

The `(repo, number)` key also means records of different kinds with the same
number cannot coexist independently. Ingestion currently merges by number too.

## Analysis writes and reviews

`SaveItemAnalysis` saves one item's thread updates, replacement relationships,
evidence, and analysed hash in a transaction. An entire multi-item analysis run
is not one transaction: earlier items remain saved if a later item fails.

Relationship uniqueness is `(repo, thread_id, item_number, rel_type)`. Evidence
rows are replaced with the supplied citation fields; the store does not prove
that cited quote text occurs in the referenced source.

Human verification is stored here, never in the vector index. Existing save logic
preserves verification, including by thread when replacing an item's analysis.
Consequently, a changed relationship type or explanation can retain an earlier
review flag. See [analysis limitations](../../analysis/README.md#current-limits)
before treating that flag as verification of the current claim.

Read methods support item/thread/relationship details, repository statistics,
and graph output. See [CLI](../../cli/README.md) and [HTTP](../../server/README.md)
for available filters and operations.
