# Local container deployment

The stack runs one Automain process with its embedded Svelte UI and one private
Qdrant 1.20.0 service. Docker Engine with Compose v2 or newer is required. The
image builds with Node 22.22.0 and Go 1.27.3; neither is needed on the host.
Frontend dependencies currently resolve from `web/package.json` without a
lockfile, so rebuilding can select newer compatible frontend packages.

## Start and inspect

From the repository root:

```sh
test -f .env || cp .env.example .env
chmod 600 .env
```

Set a nonempty `AUTOMAIN_QDRANT_API_KEY` in `.env`. Generate a random value with
`openssl rand -hex 32` and keep it private. Compose requires this key and passes
the same value to both services. Add `GITHUB_TOKEN` for imports and
`OPENROUTER_API_KEY` or dedicated provider keys only for operations that need
them. Readiness and empty-stack startup do not call model providers.

```sh
docker compose up --build --detach --wait --wait-timeout 180
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
docker compose ps
docker compose logs --follow --tail 100
```

Open <http://127.0.0.1:8080>. Only the app's loopback port is published. The API
has no authentication; do not expose it publicly without external protection.
Qdrant ports are not published. Its API key crosses only the local Docker
network, without TLS. This configuration is not for a remote Qdrant server.

`/healthz` is process liveness. `/readyz` checks local storage and Qdrant using
bounded requests without embedding or decision calls. Docker marks the app
unhealthy during a Qdrant outage; it does not automatically restart unhealthy
containers. Use logs to diagnose failures, restore Qdrant, and check readiness
again. Non-vector operations remain independent of Qdrant availability.

Task aliases are `task compose:build`, `task compose:up`, `task compose:logs`,
and `task compose:down`.

## Legacy analysis configuration

Compose explicitly forwards `AUTOMAIN_LLM_PROVIDER` and the `API_KEY`, `MODEL`,
`API_URL`, and `BASE_URL` settings under each prefix: `AUTOMAIN_LLM_`, `OPENROUTER_`,
`GEMINI_`, `OPENAI_`, and `DEEPSEEK_`. Compose falls back to OpenRouter only when
`AUTOMAIN_LLM_PROVIDER` is unset or blank; the copied `.env.example` selects DeepSeek.
Unset key, model, and endpoint overrides remain blank,
so Go applies the same provider defaults, aliases, and fallback order as on the
host. Generic endpoint overrides preserve private gateway URLs. See
[legacy analysis configuration](../internal/config/README.md#legacy-relationship-analysis)
for precedence; embedding and Jev settings remain separate.

Generic `AUTOMAIN_LLM_MODEL` and endpoint overrides apply to every chat provider.
Clear or replace them when switching providers; provider-specific settings do not
override them. The env templates leave generic overrides blank.

## State and mutation ownership

The `automain-data` named volume holds canonical `/data/automain.db`, derived
catalog `/data/index.db`, and SQLite journals. The `qdrant-data` named volume
holds Qdrant's `/qdrant/storage`. Compose prefixes volume names with its project
name. Keep the same project name across normal restarts to reuse the same data.
The app runs as UID/GID 10001; fresh app volumes inherit its writable `/data`
directory. An existing volume with different ownership needs an administrator
to correct ownership before startup.

Run one app instance. While it serves, submit mutations through its HTTP API,
not concurrent CLI indexing or sync processes sharing its SQLite files. See
the [HTTP guide](../internal/server/README.md) and
[retrieval guide](../internal/retrieval/README.md) for operation syntax.

```sh
docker compose down
docker compose up --detach --wait --wait-timeout 180
```

Shutdown preserves volumes. Never add `--volumes` or `-v` to routine shutdown.
Changing the Compose project name creates a separate empty stack, not a data
migration. The build context excludes local databases and secrets, so existing
host data is never copied into the image or automatically imported. Preserve
old `vectors.db` files and follow the retrieval guide's explicit migration
instructions before switching existing deployments.

## Consistent backup and restore

Volumes are persistence, not backups. Back up canonical state, catalog, and
Qdrant together. A straightforward consistent backup stops all writers and
archives both volumes, including SQLite journals:

```sh
BACKUP="$HOME/automain-backup-$(date +%Y%m%d-%H%M%S)"
umask 077
mkdir -p "$BACKUP"
docker compose stop automain qdrant
docker compose run --rm --no-deps -T --entrypoint tar automain -C /data -czf - . > "$BACKUP/automain-data.tgz"
docker compose run --rm --no-deps -T --entrypoint tar qdrant -C /qdrant/storage -czf - . > "$BACKUP/qdrant-data.tgz"
tar -tzf "$BACKUP/automain-data.tgz"
tar -tzf "$BACKUP/qdrant-data.tgz"
docker compose up --detach --wait --wait-timeout 180
```

Check every command succeeds before treating the archives as backups. Store
them off-host with restricted access. Keep runtime secrets separately, never
inside the image or a public archive. Do not copy only a live SQLite main file:
committed data may still be in its WAL. For an online alternative, use SQLite's
backup API for each database and Qdrant's
[snapshot API](https://qdrant.tech/documentation/database-tutorials/create-snapshot/).
Those independent snapshots do not form a cross-store transaction; quiesce
application mutations when taking a coordinated set.

Restore into a new, unused Compose project rather than overwriting a live volume:

```sh
RESTORE_PROJECT=automain-restore
docker compose --project-name "$RESTORE_PROJECT" build
docker compose --project-name "$RESTORE_PROJECT" create
docker compose --project-name "$RESTORE_PROJECT" run --rm --no-deps -T --entrypoint tar automain -C /data -xzf - < "$BACKUP/automain-data.tgz"
docker compose --project-name "$RESTORE_PROJECT" run --rm --no-deps -T --entrypoint tar qdrant -C /qdrant/storage -xzf - < "$BACKUP/qdrant-data.tgz"
docker compose --project-name "$RESTORE_PROJECT" up --detach --wait --wait-timeout 180
```

Stop the original stack first to free loopback port 8080. Verify canonical
counts, index status, and representative searches before accepting the restore.
Retain original files and volumes until that check passes. A missing or empty
Qdrant volume needs reconciliation, not deletion of canonical SQLite data; see
the retrieval guide for recovery behavior.

## Secret handling

Compose reads `.env` for interpolation but forwards only the variables listed
in `compose.yaml`. Qdrant receives only its API key and telemetry setting, never
GitHub or model-provider keys. Container path, listen address, and Qdrant host
settings override host-local `.env` values. Secrets are runtime environment
variables, visible to Docker administrators. Avoid sharing `docker inspect`
or expanded `docker compose config` output; use `docker compose config --quiet`
to validate configuration without printing secrets.
