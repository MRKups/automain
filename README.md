# Automain

Automain imports GitHub issues, pull requests, discussions, and comments so you
can search related activity and inspect persistent threads and relationships.
The Go application includes a CLI, HTTP API, and embedded Svelte UI. SQLite owns
source records and relationships; Qdrant 1.20.0 supplies semantic retrieval.

## Current state

- GitHub import and thread/relationship inspection are available through CLI,
  API, and UI. [Import coverage](docs/ingestion.md#coverage-limits) is limited.
- Text search uses Gemini embeddings through OpenRouter. `similar` reuses stored
  vectors without model calls. Both return source passages, not relationship verdicts.
- Canonical `automain.db`, metadata-only `index.db`, Qdrant storage, explicit
  legacy-vector migration, and two-service Compose deployment are implemented and
  verified with disposable fixtures, real Qdrant, and Compose. The
  [implementation plan](QDRANT_IMPLEMENTATION_PLAN.md) owns acceptance evidence.
- `decide` asks Jev explicit typed questions without writes. Automatic relationship
  policy remains open. `analyze` is a separate legacy LLM pipeline whose `vector`
  finder means TF-IDF. See [analysis limits](internal/analysis/README.md).

The version is `0.1.0-dev`. Checks made no paid model calls and migrated no live
data. They do not establish release readiness or semantic/relationship accuracy.

**Review found six open defects**, including WAL migration, missing-vector recovery,
and Compose configuration parity. See the [review follow-up](QDRANT_IMPLEMENTATION_PLAN.md#review-follow-up)
before migrating existing data or relying on recovery. Passing fixtures did not cover
these cases; the defects have not yet been fixed.

## Quick start

For the two-service stack, follow the [container guide](deployment/README.md).
It requires a local Qdrant API key in `.env`; real keys are blank in the templates.
The app has no authentication. Its default published address is loopback only.

For a host build, install Go 1.26+ and [Task](https://taskfile.dev). Node.js 20+
is needed when rebuilding the frontend. Run Qdrant 1.20.0 separately for vector
operations and configure it using the [configuration guide](internal/config/README.md).

```sh
test -f .env || cp .env.example .env
# Edit .env: set OPENROUTER_API_KEY for embeddings and Jev decisions.
# GITHUB_TOKEN is optional for public issues/PRs, required for Discussions.
task build

# Keep example data outside the development checkout.
demo_dir=$(mktemp -d "${TMPDIR:-/tmp}/automain-demo.XXXXXX")
export AUTOMAIN_DB_PATH="$demo_dir/automain.db"
export AUTOMAIN_INDEX_DB_PATH="$demo_dir/index.db"

./bin/automain status --json
./bin/automain sync --repo owner/name --limit 100 --json
./bin/automain index prepare --repo owner/name --json
./bin/automain index embed --repo owner/name --limit 100 --timeout 5m --json
./bin/automain index status --repo owner/name --json
./bin/automain search --repo owner/name --query "cache entries stay stale" --json
./bin/automain similar --repo owner/name --item 123 --json
./bin/automain decide --input internal/decision/example.json --json
./bin/automain serve --repo owner/name
```

Replace `owner/name` with your repository. `sync --limit` counts GitHub items;
`index embed --limit` counts source passages. A single item can produce many
passages. Repeat embedding runs to process remaining pending passages; completed,
unchanged passages are reused. Choose an indexed item number for `similar`.
`serve` defaults to `http://127.0.0.1:8080`.
The example retains data in `$demo_dir` for this session; configure stable paths
outside the checkout for continued use. Sync, embedding, search, and decisions can
contact live providers and incur costs. Use the disposable fixture checks for verification.

Status, sync, passage preparation, and standalone decisions do not contact Qdrant.
Preparation needs no model key. Preserve any old `vectors.db` and use the explicit
[migration workflow](internal/retrieval/README.md#legacy-vector-migration) to reuse
compatible embeddings without paid regeneration. The old `--vector-db` setting
does not select active vector storage.

## Guides

- [Documentation index](docs/README.md), [architecture](docs/architecture.md),
  and [implementation map](internal/README.md).
- [Retrieval and migration](internal/retrieval/README.md),
  [storage/recovery](internal/storage/README.md), and
  [configuration](internal/config/README.md).
- [CLI](internal/cli/README.md), [HTTP API](internal/server/README.md),
  [web UI](web/README.md), and [container deployment](deployment/README.md).

## Development

```sh
task check                 # Guard, formatting, vet, and compilation
task check:retrieval       # Retrieval fixtures; see scripts/README.md for prerequisites
task check:decisions       # Offline Jev CLI/API fixtures; no live API calls
task web                  # Rebuild frontend assets after editing web/src
task build                # Embed assets into a rebuilt Go binary
task sandbox -- status --json
```

See [development checks](scripts/README.md) for isolated verification and limits.

## License

[MIT](LICENSE)
