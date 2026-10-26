# Existing relationship analysis

[Architecture](../README.md) | [Configuration](../config/README.md)

This is the legacy `analyze` pipeline. It selects candidate **Automain threads**,
asks an LLM for relationship decisions, normalizes the response, and saves threads,
relationships, and evidence in the source database. It does not consume the new
Gemini embedding index.

```sh
automain analyze --repo owner/name --limit 10 --finder fts --json
automain analyze --repo owner/name --item 42 --reanalyze --json
```

By default, analysis processes dirty items (source hash differs from analysed
hash). `--reanalyze` includes unchanged items. `--item` selects one item, which is
still skipped if unchanged unless reanalysis is requested. Without a limit, all
eligible items can be processed and incur model costs.

## Candidate finders

| `--finder` | What it actually does |
| --- | --- |
| `llm` (default) | LLM selection from existing thread summaries |
| `fts` | In-memory BM25-style lexical ranking with tokenization/stemming |
| `vector` | In-memory TF-IDF word/bigram/character-feature cosine ranking |

The `fts` finder is not a SQLite FTS index. The `vector` finder is **not**
`google/gemini-embedding-2`, does not use `vectors.db`, and makes no embedding API
request. All three still use the relationship LLM after candidate selection.
The default candidate cap is 10; lexical finders have a small-repository fallback.

The analyser receives the item/comments and selected thread member items. It can
match existing threads or propose new ones. Normalization validates referenced
threads, relationship types, confidence, and evidence source identities. IDs are
deterministically derived from repository/title or relationship tuple. The app
saves at most one relationship per item/thread in each analysis decision.

## Long prompt inputs

Unlike the new [retrieval chunker](../retrieval/README.md#long-inputs-and-chunking),
this pipeline truncates text when building prompts:

| Input | Character limit |
| --- | --- |
| Analysed item body | 6000 |
| Each analysed comment | 2000 |
| Candidate member item body | 1500 |
| Finder item body/features | 4000 |

These are character limits, not token counts. There is no shared embedding-byte
guard or whole-prompt token budget here. Many comments/candidates can still
produce a large prompt, and omitted source tails are not analysed separately.

## Current limits

- Evidence validation checks source item/comment IDs and required fields, but
  **does not check that quote text occurs in the source**. Citation validation
  must not be described as proof of a verbatim quote. This is a validation gap,
  not evidence that a live provider has fabricated a quote.
- Supplied author/URL fields are filled from source only when absent, not always
  replaced with canonical metadata.
- Reanalysis can retain human verification by thread even when the relationship
  type, explanation, or evidence changes. Review flags are not tied to a claim
  fingerprint. Recheck changed claims before relying on those flags.
- Model confidence is a categorical output, not a calibrated probability.
- Saves are per item; earlier items remain committed if a later item fails.

Semantic search returns exact, freshness-checked passages without these model
decisions. [Jev plumbing](../decision/README.md) is wired separately through `decide`.
It returns typed judgments and probabilities without prose, threads, or writes.
Automatic search composition and relationship policy remain open; legacy `analyze`
has not been silently switched to Jev and is not used as its fallback.
