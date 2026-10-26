# Storage operations

[Architecture](../README.md)

Automain keeps canonical records, derived passage metadata, and vectors separate.
The Qdrant/catalog split is implemented and verified with disposable fixtures and
container/recovery checks. See the [active plan](../../QDRANT_IMPLEMENTATION_PLAN.md)
for evidence; no production migration is claimed.

| Store | Default | Contents | Authority |
| --- | --- | --- | --- |
| [Source SQLite](sqlite/README.md) | `data/automain.db` | Imported items/comments, threads, relationships, evidence, human verification, sync metadata | Canonical, not safely disposable |
| [Catalog SQLite](catalog/README.md) | `index.db` beside source DB | Copied source passages, spaces, pending/indexed receipts, deletion tombstones; no vector blobs | Derived, not canonical source text |
| [Qdrant 1.20.0](qdrant/README.md) | `127.0.0.1:6334` gRPC | Vectors and searchable passage payloads, one collection per space | Derived vector index |
| [Legacy SQLite reader](vector/README.md) | Explicit migration input | Preserved schema-v1 `vectors.db` passages and embeddings | Read-only migration source, never the active index |

Use `--db` / `AUTOMAIN_DB_PATH` for canonical SQLite and `--index-db` /
`AUTOMAIN_INDEX_DB_PATH` for the catalog. An explicit catalog path remains explicit
when the source path changes; otherwise it follows the source directory. Qdrant
settings are separate; see [configuration](../config/README.md). Deprecated
`--vector-db` / `AUTOMAIN_VECTOR_DB_PATH` is legacy/report-only configuration. It
does not choose active vector storage or trigger migration; migration requires
`index migrate --from <path>`.

```sh
scratch=$(mktemp -d "${TMPDIR:-/tmp}/automain-storage.XXXXXX")
automain index prepare --repo owner/name \
  --db "$scratch/automain.db" --index-db "$scratch/index.db" --json
```

Source, catalog, and explicit legacy files must not alias, including symlinks and
hard links. Catalog and legacy files have different SQLite application IDs and
separate schemas; neither is silently upgraded into the other. The source adapter
rejects nonzero application IDs, but its historical ID 0 is not a generic ownership
check for unrelated files. Always choose dedicated paths. Startup opens source
and catalog files and constructs a lazy Qdrant client without contacting it.

## Backups and rebuilds

- Back up the **source database** to retain relationship decisions and reviews.
  A fresh GitHub import cannot recover human verification or all historical text.
- Both active SQLite adapters use WAL mode. Do not copy only a live `.db` file and
  assume all writes are included. Use SQLite-aware backup tooling, or stop all writers and
  ensure a clean checkpoint/close before making filesystem copies.
- Catalog and Qdrant payloads contain copied source text too. Apply the same
  access/privacy controls as for canonical SQLite. The app does not encrypt the
  SQLite files.
- Back up catalog receipts and Qdrant vectors with canonical state. Independent
  snapshots are not a cross-store transaction; stop mutations for a coordinated
  set. See [container backup/restore](../../deployment/README.md#consistent-backup-and-restore).
- Preserve old vector caches and their required WAL/SHM sidecars. Explicit
  [migration](../retrieval/README.md#legacy-vector-migration) reuses compatible
  embeddings without model calls. Never remove a WAL to force a migration.
- A new catalog path alone is not an isolated new vector index. For an independent
  rebuild, also choose a new Qdrant collection prefix or separate Qdrant storage,
  retain the old stores, then prepare and embed repeatedly. Recreating missing
  vectors can incur provider costs; prefer recovery or compatible migration first.

## Recovery and progress

There is no shared transaction across source SQLite, catalog SQLite, and Qdrant.
Catalog receipts track writes before remote submission, confirm only completed
current writes, and retain deletions until acknowledged. Sync changes canonical
records but does not refresh the index automatically. Search checks both catalog
and current canonical text before returning passages.

`index prepare` works without Qdrant or model keys, reconciling local passages and
recording obsolete point identities for cleanup. `index reconcile` also contacts
Qdrant, completes queued deletions, and returns missing indexed points to pending
without model calls. It cannot reconstruct a lost vector. Restore a backup or
migrate compatible legacy vectors before approving paid regeneration through
`index embed`. Embedding checks pending remote presence to reuse writes that
completed before a receipt was confirmed. See [retrieval](../retrieval/README.md).

Status counts are local receipts, not a continuous remote audit. `/readyz` probes
source/catalog/Qdrant availability, not completeness. A lost Qdrant volume needs
reconciliation, not deletion of source data, catalog files, or preserved caches.

Index rebuilds never modify source records or reviewed relationships. Conversely,
index freshness says nothing about whether legacy analysis is up to date.
