# Legacy SQLite migration reader

[Storage operations](../README.md) | [Retrieval](../../retrieval/README.md)

This package implements only `domain.LegacyIndex`. `OpenLegacy(ctx, path)` reads
an existing SQLite vector index for explicit migration. It is not a production
catalog or vector backend. Qdrant owns production vector storage.

The reader makes no provider calls, reads no environment files, and does not
initialize databases, apply migrations, change journal mode, or write passages/vectors.
Its caller owns `Close` and the private snapshot's lifetime.

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

Stop the old writer before migration and keep the source files unchanged while
opening the reader. This is a stopped-file copy, not an online backup protocol.
An existing `-journal` rollback sidecar is rejected, even if empty: omitting a hot
journal can expose uncommitted pages. Preserve the originals and recover a complete
disposable copy with SQLite, or use a consistent backup. Never delete the original
journal to force migration.
The reader copies the main file and any existing `-wal` and `-shm` files read-only
into a private, mode-0700 temporary directory. Only absent sidecars are optional;
existing inputs must be readable regular files. The originals are never opened
through SQLite, checkpointed, chmodded, deleted, or given new directory entries,
including when opening fails or copying is canceled.

SQLite opens only the copy with `mode=ro`. It may create or rebuild sidecars there,
so clean WAL-mode databases without sidecars and committed WAL data without SHM
remain readable. The reader does not use `immutable=1`, which can ignore WAL data.
Other temporary SQLite work stays in memory. Preserve existing database/WAL/SHM
files together, or use a consistent SQLite backup. Never remove the original WAL
to force migration.

Every open needs fresh temporary storage for the main database and existing
sidecars, plus space for SQLite's copied-sidecar work. Ensure the OS temporary
directory (selected by `TMPDIR` where supported) is writable and has enough free
space. Copying checks cancellation between bounded chunks. Failed opens and
`Close` remove only that reader's private directory; cleanup failures report its
path for manual removal after the reader closes. A process crash can leave a
private copy behind. No automatic cleanup touches the originals.

All batches read the same private file set until `Close`. Each batch uses one
connection and one read transaction, including its space lookup and embedding
rows. There is no shared transaction with the canonical database, catalog, or
Qdrant. A later open takes a fresh copy.

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
