# GitHub ingestion

[Architecture](../README.md) | [Source storage](../storage/sqlite/README.md)

This adapter implements `domain.GitHubSource`. Sync imports source text; it does
not embed it, analyze relationships, or run automatically in the background.

```sh
automain sync --repo owner/name --limit 100 --json
automain sync --repo owner/name --since 2026-10-01T00:00:00Z --json
```

## Imported sources

- REST fetches issues and PRs, their regular issue comments, and PR merge state.
- GraphQL fetches discussions, discussion comments, and their replies.
- Sources retain title/body, state, author, labels where available, URL, and
  timestamps. Replies use the normal comment representation.
- Missing authors normalize to `ghost`; labels and content hashes are normalized
  deterministically. Source text is retained for later passage preparation.

`GITHUB_TOKEN` is optional for public REST access but required for GraphQL
Discussions. Without it, sync skips discussions and returns a warning. Private
repositories require credentials with appropriate access.

## Pagination, limits, and cutoffs

REST pages use up to 100 records. Discussion and top-level discussion-comment
connections are paginated; nested reply coverage has the limit below. REST detail
hydration uses up to four workers. The selected items and their comments are
returned as a batch for transactional persistence.

`--limit` counts items, not comments. Candidates from REST and GraphQL are merged
by number, sorted newest-updated first, and limited before REST detail hydration.
Omitting the limit (or using 0) imposes no item cap. Repeated small limited runs
are not a cursor-based way to exhaust an older backlog.

`--since` is an explicit RFC3339 cutoff. `--full` ignores that cutoff. A sync
watermark is stored and read, but currently is **not used automatically as the
next fetch cutoff**. Do not assume an ordinary sync is an incremental resume.

## Current limits

- No PR diffs, changed-file contents, review threads, or inline review comments
  are fetched. Search covers only text actually imported into the local database.
- Each discussion comment fetches only its first **100 replies**; reply pagination
  is not implemented. Discussion labels are limited to the first 50.
- Imports do not reconcile upstream deletions. Missing/deleted comments are not
  removed from local storage. A full sync does not fix this.
- Items are keyed/merged by repository and number, not kind plus number. An
  issue/PR and discussion with the same number can collide instead of remaining
  distinct sources.
- No scheduled polling, webhook processing, or automatic embedding follows sync.

These are import limitations, separate from
[long-input splitting](../retrieval/README.md#long-inputs-and-chunking). Splitting
preserves imported text; it cannot recover content that was never fetched.
