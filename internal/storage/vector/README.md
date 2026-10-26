# Legacy SQLite migration reader

[Storage operations](../README.md) | [Retrieval](../../retrieval/README.md)

This package implements only `domain.LegacyIndex`. `OpenLegacy(ctx, path)` reads
an existing SQLite vector index for explicit migration. It is not a production
catalog or vector backend. Qdrant owns production vector storage.

The reader makes no provider calls, reads no environment files, and does not
initialize databases, apply migrations, change journal mode, or write passages/vectors.
Its caller owns `Close`. The filesystem read-only guarantee has the open defect below.

## Accepted files and ownership

Pass a nonempty regular SQLite file, not a URI or memory database. Missing files,
source/catalog databases, unrelated files, missing tables/columns, and unsupported
migration histories fail explicitly. The required identity is application ID
`1096173912`, with only schema version 1 in `vector_schema_migrations`.
[`schema.go`](schema.go) retains the historical schema description, not an
initialization path.

Invalid paths, wrong ownership, and incompatible schemas return `invalid_input`
(HTTP 400, CLI exit 2). Permission, corruption, and I/O failures return
`external_unavailable` (HTTP 502, CLI exit 1), with the underlying cause preserved.

SQLite opens with `mode=ro` and `readonly_shm=1`. The latter prevents writes to
shared-memory sidecars while retaining committed WAL data, unlike `immutable=1`,
which can ignore the WAL. Temporary SQLite work stays in memory. A WAL-mode
database can require an existing readable SHM sidecar. Missing sidecars or a WAL
that SQLite cannot safely read produce an error rather than a partial snapshot.
Preserve the database, WAL, and SHM together, or use a consistent SQLite backup.
Never remove the WAL to force migration.

**Known defect (R1):** opening a valid WAL-mode database after a clean writer shutdown
can create an empty `-wal` sidecar and then fail. The main database stayed unchanged
in the reproduction, but `mode=ro` and `readonly_shm=1` do not provide the promised
filesystem boundary. Do not migrate your only copy. Preserve a consistent backup
and wait for the [review fix](../../../QDRANT_IMPLEMENTATION_PLAN.md#review-follow-up)
before relying on this migration path.

Stop the old writer before migration. Each batch uses one connection and one
read transaction, including its space lookup and embedding rows. There is no
cross-batch snapshot or transaction with the canonical database, catalog, or
Qdrant. A changing legacy index cannot provide repeatable keyset traversal.

## Reading and validation

`ListChunks` returns the exact stored passage payload. `ReadEmbeddings` returns
that payload with its stored vector. Both require a valid repository and batch
limit of **1-10000**, order by passage ID, and accept an exclusive `afterID`
cursor. Repository filtering applies before pagination.

`Spaces` returns every registered space, including spaces without vectors in the
requested repository. Provider, model, revision, and dimensions must validate,
and their derived space ID must match the stored ID. Unknown recipes are not
replaced or inferred. The caller decides compatibility; unsupported spaces must
be reported explicitly rather than silently omitted.

Vectors are decoded unchanged from **little-endian float32 BLOBs**, four bytes
per dimension. The old writer normalized them before storage; the reader does
not normalize or regenerate them. Wrong lengths, non-finite/zero vectors,
dimension mismatches, orphan references, invalid passage metadata, and embedding
fingerprints that disagree with their legacy passages fail explicitly. A failed
batch returns no partial result.

The reader does not establish current canonical-source compatibility or recompute
historical fingerprints under a guessed recipe. Retrieval must prepare current
canonical passages, import only matching identities/fingerprints, and report
skips and reused embeddings. Keep original files/backups until migration counts,
fingerprints, repeat-run reuse, and search results have been verified.
