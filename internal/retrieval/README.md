# Source preparation and semantic search

[Architecture](../README.md) | [Embedding adapter](../embedding/README.md) |
[Catalog](../storage/catalog/README.md) | [Qdrant](../storage/qdrant/README.md)

This package turns stored source text into searchable passages, coordinates
embedding work, and returns matching passages. It does not classify relationships,
generate explanations or quotes, or change human verification.

Preparation and status use a passage catalog; embedding and search additionally
require a vector store. The two ports are injected separately. Missing vector
storage fails before a provider request and does not prevent preparation through
an available catalog. Production uses metadata-only SQLite `index.db` and Qdrant
1.20.0 through the official Go gRPC client. The legacy SQLite vector package is
read-only migration input, not a fallback. Real-Qdrant fixtures and container/recovery
checks passed without paid model calls; the
[active plan](../../QDRANT_IMPLEMENTATION_PLAN.md) owns acceptance evidence. No live
data migration, semantic-quality evaluation, or release readiness is claimed.

## Long inputs and chunking

Embedding inputs above roughly 8K tokens can be silently truncated upstream.
Automain does not send a whole long PR or discussion as one embedding input.
[`chunks.go`](chunks.go) splits each source independently:

- Item title, item body, and each individual comment are separate sources.
- Windows contain at most **1,200 Unicode code points**, with **150-code-point
  overlap**. The last window may be shorter. Whitespace-only windows are skipped.
- Text is copied exactly, with UTF-8-safe start/end **byte offsets** into the
  original title, body, or comment. End offsets are exclusive.
- Source references retain repository, item number/kind, comment ID where
  applicable, author, and URL. Titles are indexed even if the body is empty.

These are character windows, not Gemini token counts. They do not respect
paragraphs, headings, sentences, or code blocks. Comments are never concatenated
into one large conversation input. Imported discussion replies are comments too.

The provider receives `title: <item title> | text: <passage>` for every passage.
The [embedding adapter](../embedding/README.md#input-safety) independently rejects
any complete formatted input above **8,000 UTF-8 bytes**, including title context
or query instructions. It does not silently truncate. An unusually long title
can still exceed this budget because the full title is repeated as context;
that batch fails explicitly rather than shortening the title automatically.
Search queries are rejected when oversized, not split into multiple queries.

This protects the embedding path only. The [legacy analyser](../analysis/README.md)
uses separate prompt truncation and does not use these passages.

## Prepare, embed, inspect

```sh
# Keep both databases outside the development checkout.
scratch=$(mktemp -d "${TMPDIR:-/tmp}/automain-retrieval.XXXXXX")
export AUTOMAIN_DB_PATH="$scratch/automain.db"
export AUTOMAIN_INDEX_DB_PATH="$scratch/index.db"
# Sync source data here before indexing it; configure a separate Qdrant service.
automain index prepare --repo owner/name --json
automain index embed --repo owner/name --limit 100 --timeout 5m --json
automain index status --repo owner/name --json
```

`prepare` reads the complete locally stored repository, builds all its passages,
then reconciles them in one local catalog transaction. It makes no network or
model calls.
If reading a source fails, it does not replace the index with a partial snapshot.
The snapshot reflects local storage, not a fresh fetch from GitHub.

`embed` validates provider configuration, prepares passages, registers the selected
embedding space, and reconciles remote presence/deletions before pending work.
It checks every catalog-indexed passage for remote presence across registered
spaces. Pending passages already present with matching payloads are reused;
only missing vectors are requested, in batches of up to 32. The work limit defaults
to 100, accepts 1-10000, and counts newly embedded plus reused pending passages,
not items or the cost of scanning existing receipts. Repeat to finish pending work.

Changed or removed passages invalidate receipts across spaces and leave durable
tombstones for old point versions, including tracked in-flight writes. A title
change also changes body/comment input fingerprints because title context is
included. Each write is tracked in the catalog before remote submission. It is
confirmed indexed only after Qdrant reports completion and canonical source
freshness is rechecked. A changed source causes an explicit error and cleanup
state, not a current-looking receipt.

A failed batch does not erase earlier progress. After interruption, rerun the
bounded operation; matching remote points can be reused without new embeddings.
There is no source/catalog/Qdrant transaction or cross-process job reservation.
Use one mutation owner; while serving, submit mutations through the API rather
than independent CLI writers sharing the files.

Index status reports prepared passages, pending deletion count, and embedded/
pending receipts by registered space. Space registration can appear before the
first successful embedding save. Status does not prepare, audit remote presence,
or establish canonical freshness. `/readyz` checks availability, not completeness.

The configured Gemini output defaults to 3072 dimensions and supports 128-3072.
Model, provider, input-recipe revision, and dimensions identify a space. Compatible
legacy spaces retain their existing size and identity during migration; changing
configuration selects another space rather than converting stored vectors.

## Reconciliation and recovery

```sh
automain index reconcile --repo owner/name --json
```

Reconciliation prepares local passages, resumes durable Qdrant deletions, and
marks catalog-indexed points that are missing remotely as pending. It makes no
embedding, Jev, or chat calls and needs no provider key. It does not recreate lost
vectors. Restore Qdrant or migrate compatible legacy vectors first if available;
`index embed` can regenerate genuinely missing vectors at provider cost.

Payload-only points are an exception: current presence checks do not detect a
missing dense vector, so reconciliation and migration cannot repair that state.
See the [Qdrant recovery defect](../storage/qdrant/README.md#storage-and-queries).

Preparation works during Qdrant outages; reconcile/embed/search/similar fail clearly
without hidden lexical, SQLite-vector, or LLM fallback. Restore Qdrant, reconcile,
and retry. Never delete source data or an existing vector cache to recover.

## Legacy-vector migration

Stop the old writer and preserve the original `vectors.db`, required WAL/SHM
sidecars, and a consistent backup. Keep source, new catalog, and legacy input paths
distinct. With the current canonical source database selected and Qdrant available:

```sh
automain index migrate --repo owner/name --from /path/to/preserved/vectors.db \
  --limit 100 --json
automain index status --repo owner/name --json
automain index reconcile --repo owner/name --json
```

The old database uses a read-only SQLite connection and is not initialized,
rewritten, or deleted. However, cleanly closed WAL-mode inputs can currently fail
after an empty sidecar is created. See the [legacy reader defect](../storage/vector/README.md#accepted-files-and-ownership)
before attempting migration; do not use your only copy or remove the WAL. Deprecated `--vector-db` does not
activate migration or select production vector storage.

Migration prepares the current catalog, then copies only vectors whose passage
identity/content fingerprint still matches. It preserves space identities and
vector dimensions, uses existing matching remote points, and makes zero model
calls. Stale or absent canonical passages increment `skipped_chunks`; malformed
vectors or invalid metadata fail explicitly rather than being regenerated.
`migrated_chunks` counts new remote vector writes, `reused_chunks` counts matching
remote vectors, and `spaces` counts discovered legacy spaces. The default limit is
100 new writes per run, with range 1-10000. Repeat runs scan past completed entries
to progress; reused/skipped counts are per run, not cumulative unique totals.

Verify counts, fingerprints, representative searches/similarity, and repeat-run
reuse before accepting the migration. To query an older configured size, select
its matching embedding configuration; the 3072 default does not convert it.
Keep the original cache/backups after cutover. Migration never pays to regenerate
skipped vectors; later `index embed` is a separate, potentially paid operation.

## Search

```sh
automain search --repo owner/name --query "cache entries stay stale" --limit 10 --json
```

Search requires an embedding key and available Qdrant. When the catalog has no
indexed receipts for the selected repository/space, it returns no matches without
paying to embed the query.
Otherwise each search embeds its query; there is no query cache.
If receipts indicate indexed data but its Qdrant collection is missing, search
fails with `external_unavailable` (HTTP 502), not an empty success. Restore Qdrant
data or run `index reconcile` before choosing migration or explicit regeneration.

Vectors are compared only within the selected repository and embedding space.
Search uses exact, unquantized Qdrant cosine ranking, defaults to 10 passages,
and allows up to 100.
Results are passages, so one item can appear multiple times and overlapping
windows can repeat text. There is no item deduplication, reranking, relevance
threshold, or relationship decision stage.

Before returning a candidate, the service checks its catalog fingerprint, reloads
its current canonical record, and regenerates the selected source's passage windows
to check identity, window boundaries, title context, and exact fingerprint. This
also detects appended text that changes a previously final passage.
Missing or changed sources are omitted; author and URL metadata are refreshed.
Filtering happens after top-K selection without backfilling, so results may be
fewer than requested. This is a freshness check against local data at read time,
not an atomic snapshot across the stores or a check against live GitHub.

After source updates, run `index embed` again to reconcile and fill pending work.
Cosine scores are similarity values, not confidence or confirmed relationships.

## Stored-vector similarity

```sh
automain similar --repo owner/name --chunk <passage-id> --limit 10 --json
automain similar --repo owner/name --item 123 --limit 10 --json
```

Choose exactly one prepared passage ID or positive item number. Similarity uses
stored vectors in the configured embedding space without embedding, Jev, or chat
calls; no model-provider key is needed. Repository/space and canonical freshness
are checked for query points as well as returned candidates. A missing vector for
an explicit passage fails; item queries skip selected passages without vectors
and fail if none remain. Refresh stale prepared sources before querying.

Item queries select at most 32 available stored passage vectors in deterministic
passage-ID order, not relevance order. Selection continues past prepared passages
whose vectors are missing. `truncated: true` reports that more than 32 available
query passages were found; `query_passages` reports how many vectors were queried.
This is not exhaustive coverage of a long PR or discussion.

Results exclude the source item and group candidates by item, with up to three
distinct supporting passages each. An item's score is its highest retained passage
score. The default result limit is 10 items, with range 1-100. Candidate retrieval
per query is bounded before freshness filtering/grouping, so fewer items may be
returned; there is no completeness, recall, or relationship-quality guarantee.

## Troubleshooting

| Symptom | Check or next action |
| --- | --- |
| `not_configured` | Set `OPENROUTER_API_KEY` or the dedicated embedding key; analysis keys are separate. |
| Passages exist but `spaces` is empty | Preparation has run without registering a space. Run `index embed` or explicit legacy migration. |
| A new endpoint/dimension setting returns no matches | It selects a separate space. Embed that space before searching it. |
| Fewer or no matches after a sync | Run `index embed` to reconcile source changes and fill pending work; repeat until the selected space has no pending passages. |
| A run fails partway through | Correct the reported failure, then rerun. Earlier saved batches are reused; requests are not automatically retried. |
| Catalog counts look indexed after Qdrant data loss | Restore Qdrant if possible, then run `index reconcile`. Missing points become pending; reconciliation does not regenerate vectors. |
| Qdrant is unavailable | Restore the configured service/key/TLS settings, then reconcile and retry. Non-vector operations remain independent. |
| Input exceeds 8000 bytes | It was rejected before submission, not truncated. Check query length or repeated title context; increasing the work limit does not fix an oversized input. |

Use `automain status --json` to inspect the selected model, dimensions, endpoint,
and key-presence flag, and `automain index status --repo owner/name --json` for
stored-space counts. `--limit` on embedding controls total work in that run, not
passage size or the provider's maximum batch size.
