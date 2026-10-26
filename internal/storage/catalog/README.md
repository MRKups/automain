# Passage catalog

This adapter implements the domain `ChunkCatalog` in a separate metadata-only
SQLite `index.db`. It rejects source, legacy vector, unrelated, and unsupported
catalog databases. It stores passages, embedding spaces, and durable remote-write
and deletion receipts, never vectors. The composition root owns `Open` and `Close`.

Replacement reconciles a complete repository snapshot atomically. Changed and
removed passages leave deletion tombstones for every tracked old version,
including unconfirmed writes. Current indexed receipts alone count as embedded;
registered spaces appear in status even before any write is confirmed.

Page limits accept 1 to 10,000 passages or deletions. Receipt mutations accept
1 to 256 entries per batch. Complete repository replacement has no batch cap
and accepts an empty snapshot to remove all prepared passages.

The mutation owner must track before remote writes, confirm only completed writes,
and finish remote deletions before completing their tombstones. Repeated tracking
does not downgrade indexed receipts. Reverting to an earlier content hash can
revive its delete marker as pending, so callers must serialize mutations and clean
remote deletions before tracking that version again. This package does not provide
distributed coordination or verify remote presence. See the
[retrieval guide](../../retrieval/README.md) for application orchestration.
