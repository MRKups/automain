# Web UI

[Project overview](../README.md) | [HTTP API](../internal/server/README.md)

The Svelte 5 application in [`src/App.svelte`](src/App.svelte) uses the Go HTTP API
through same-origin `/api/...` requests. `automain serve` serves its compiled
assets from the Go binary; production use does not need a separate Node server.

## Existing screens

- Threads and their member items/relationships.
- Relationships, evidence, filters, and human-verification controls.
- Imported items and comments.
- Graph view.
- Status/configuration and repository statistics.

Sync and analyze controls invoke the existing source-import and legacy LLM
analysis operations. **There is no embedding-index or semantic-search screen
yet.** Use the [CLI](../internal/cli/README.md) or
[HTTP retrieval endpoints](../internal/server/README.md#retrieval-examples).
The analysis UI does not switch to embeddings when a finder is called `vector`.

Evidence displayed by the UI is stored legacy analysis output. Its source-ID
validation does not prove quote occurrence, and review flags can survive changed
claims. See [analysis limits](../internal/analysis/README.md#current-limits).

## Build and run

From the repository root, with Node.js 20+:

```sh
task web
task build
./bin/automain serve --repo owner/name
```

`task web` installs npm dependencies if `web/node_modules` is missing, runs Vite,
and sanitizes generated prose punctuation with
[`scripts/sanitize-dist.mjs`](scripts/sanitize-dist.mjs). Assets go to `web/dist`;
sourcemaps are disabled. [`embed.go`](embed.go) embeds that directory at Go build
time. `task build` alone does **not** rebuild Svelte sources. A running binary
must be rebuilt/restarted to serve changed assets.

`npm --prefix web run dev` starts Vite, but the current Vite configuration has no
`/api` proxy. It is not a standalone full-stack development setup: same-origin
API requests need routing to the Go server. The build-and-serve workflow above
works without adding such routing.

`task check` builds Go against existing assets; it does not compile Svelte or
exercise browser interactions. There is no frontend test script configured.
