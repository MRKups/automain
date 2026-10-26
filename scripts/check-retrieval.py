#!/usr/bin/env python3
"""Black-box retrieval checks with real Qdrant and a loopback OpenRouter fixture."""

from contextlib import contextmanager
import hashlib
import http.server
import json
import math
import os
from pathlib import Path
import selectors
import sqlite3
import struct
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


QDRANT_IMAGE = "qdrant/qdrant:v1.20.0"
QDRANT_KEY = "fixture-only-qdrant-key"
PROVIDER_KEY = "fixture-only-openrouter-key"
REPO = "check/fixture"
OTHER_REPO = "check/other"


def request_json(url, payload=None, method=None, headers=None, status=200):
    data = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(url, data=data, method=method,
                                     headers={"Content-Type": "application/json", **(headers or {})})
    try:
        response = urllib.request.urlopen(request, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read().decode()
        assert response.status == status, (url, response.status, body)
        return json.loads(body) if body.startswith(("{", "[")) else body


@contextmanager
def qdrant_container():
    """Own exactly one disposable container, with no host or persistent volumes."""
    try:
        available = subprocess.run(["docker", "info", "--format", "{{.ServerVersion}}"],
                                   capture_output=True, text=True, timeout=20)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SystemExit("Retrieval checks require Docker and a running daemon. Start Docker/OrbStack and retry.") from error
    if available.returncode:
        raise SystemExit("Retrieval checks require a running Docker daemon. Start Docker/OrbStack and retry.\n" + available.stderr)
    print("Docker server: " + available.stdout.strip(), flush=True)
    launched = subprocess.run(
        ["docker", "run", "--rm", "-d", "-p", "127.0.0.1::6333", "-p", "127.0.0.1::6334",
         "-e", "QDRANT__SERVICE__API_KEY=" + QDRANT_KEY, QDRANT_IMAGE],
        capture_output=True, text=True, timeout=300,
    )
    if launched.returncode:
        raise SystemExit("Could not launch pinned Qdrant fixture; check Docker/image access and retry.\n" + launched.stderr)
    container = launched.stdout.strip()
    try:
        def port(number):
            result = subprocess.run(["docker", "port", container, f"{number}/tcp"],
                                    capture_output=True, text=True, check=True, timeout=10)
            return int(result.stdout.strip().rsplit(":", 1)[1])

        rest = f"http://127.0.0.1:{port(6333)}"
        grpc = port(6334)
        deadline = time.monotonic() + 45
        while True:
            try:
                request_json(rest + "/readyz", headers={"api-key": QDRANT_KEY})
                break
            except (OSError, AssertionError):
                if time.monotonic() >= deadline:
                    logs = subprocess.run(["docker", "logs", container], capture_output=True, text=True, timeout=10)
                    raise RuntimeError("Qdrant readiness timed out.\n" + logs.stdout + logs.stderr)
                time.sleep(0.2)
        assert request_json(rest + "/", headers={"api-key": QDRANT_KEY})["version"] == "1.20.0"
        denied = urllib.request.Request(rest + "/collections")
        try:
            urllib.request.urlopen(denied, timeout=5).close()
        except urllib.error.HTTPError as error:
            assert error.code in (401, 403), error.code
        else:
            raise AssertionError("Qdrant fixture accepted unauthenticated collection access")
        yield rest, grpc
    finally:
        cleanup = subprocess.run(["docker", "rm", "-f", container], capture_output=True, text=True, timeout=30)
        assert cleanup.returncode == 0, "Could not remove owned Qdrant fixture: " + cleanup.stderr


def fixture_vector(text, dimensions):
    weight = (int.from_bytes(hashlib.sha256(text.encode()).digest()[:4], "big") + 1) / 2**32
    return [1.0, weight] + [0.0] * (dimensions - 2)


@contextmanager
def openrouter_fixture():
    state = {"mode": "ok", "requests": [], "errors": [], "on_request": None}

    class Mock(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            try:
                request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                state["requests"].append(request)
                assert self.path == "/api/v1/embeddings", self.path
                assert self.headers.get("Authorization") == "Bearer " + PROVIDER_KEY
                assert request["model"] == "google/gemini-embedding-2"
                assert request["encoding_format"] == "float"
                assert 1 <= len(request["input"]) <= 32
                dimensions = request["dimensions"]
                if state["on_request"]:
                    callback, state["on_request"] = state["on_request"], None
                    callback()
                data = [{"index": i, "object": "embedding", "embedding": fixture_vector(text, dimensions)}
                        for i, text in enumerate(request["input"])]
                response = {"object": "list", "model": request["model"], "data": list(reversed(data))}
                mode, status = state["mode"], 200
                if isinstance(mode, int):
                    status = mode
                    response = {"error": {"message": "Untrusted error containing " + PROVIDER_KEY}}
                elif mode == "wrong_model":
                    response["model"] = "some/other-model"
                elif mode == "bad_count":
                    response["data"] = []
                elif mode == "missing_index":
                    del data[0]["index"]
                elif mode == "duplicate_index":
                    response["data"] = [data[0], data[0]]
                elif mode == "wrong_dimensions":
                    data[0]["embedding"] = [1.0, 0.0]
                elif mode == "zero":
                    data[0]["embedding"] = [0.0] * dimensions
                elif mode == "null":
                    data[0]["embedding"][0] = None
                elif mode == "huge_number":
                    data[0]["embedding"][0] = 1e100
                body = json.dumps(response).encode() if mode != "malformed" else b"not JSON"
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                if status == 307:
                    self.send_header("Location", "/must-not-follow")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            except Exception as error:
                state["errors"].append(repr(error))
                self.send_error(500)

    mock = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Mock)
    worker = threading.Thread(target=mock.serve_forever, daemon=True)
    worker.start()
    try:
        yield state, f"http://127.0.0.1:{mock.server_port}/api/v1"
        assert not state["errors"], state["errors"]
    finally:
        mock.shutdown()
        mock.server_close()
        worker.join(timeout=5)


def catalog_chunks(path, repo=REPO):
    with sqlite3.connect(path) as db:
        db.row_factory = sqlite3.Row
        rows = [dict(row) for row in db.execute("SELECT * FROM chunks WHERE repo=? ORDER BY id", (repo,))]
    for row in rows:
        row["repository"] = row.pop("repo")
    return rows


def point_id(chunk):
    data = json.dumps([chunk["repository"], chunk["id"], chunk["content_hash"]], separators=(",", ":")).encode()
    value = bytearray(hashlib.sha256(data).digest()[:16])
    value[6] = (value[6] & 15) | 128
    value[8] = (value[8] & 63) | 128
    return str(uuid.UUID(bytes=bytes(value)))


def cosine(left, right):
    return sum(a * b for a, b in zip(left, right)) / math.sqrt(sum(a*a for a in left) * sum(b*b for b in right))


class Remote:
    def __init__(self, rest, prefix):
        self.rest, self.prefix = rest, prefix

    def call(self, path, payload=None, method=None):
        return request_json(self.rest + path, payload, method, {"api-key": QDRANT_KEY})

    def collection(self, dimensions=3072):
        names = self.call("/collections")["result"]["collections"]
        selected = []
        for entry in names:
            name = entry["name"]
            if name.startswith(self.prefix):
                config = self.call("/collections/" + name)["result"]["config"]
                if config["params"]["vectors"]["size"] == dimensions:
                    selected.append(name)
        assert len(selected) == 1, selected
        return selected[0]

    def points(self, dimensions=3072):
        return self.call("/collections/" + self.collection(dimensions) + "/points/scroll",
                         {"limit": 1000, "with_payload": True, "with_vector": True})["result"]["points"]


def main():
    root = Path(__file__).resolve().parents[1]
    temporary_root = Path(os.environ.get("TMPDIR", tempfile.gettempdir()))
    if (temporary_root / "opencode").is_dir():
        temporary_root /= "opencode"
    with tempfile.TemporaryDirectory(prefix="automain-retrieval-", dir=temporary_root) as directory:
        sandbox = Path(directory)
        binary = sandbox / "automain"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/automain"], cwd=root, check=True)
        env = {key: value for key, value in os.environ.items()
               if not key.startswith(("AUTOMAIN_", "GITHUB_", "OPENAI_", "GEMINI_", "DEEPSEEK_", "OPENROUTER_", "TYPESAFE_"))}
        source, catalog = sandbox / "source.db", sandbox / "index.db"
        prefix = "fixture_" + uuid.uuid4().hex
        env.update(AUTOMAIN_DB_PATH=str(source), AUTOMAIN_INDEX_DB_PATH=str(catalog), AUTOMAIN_REPO=REPO,
                   AUTOMAIN_QDRANT_HOST="127.0.0.1", AUTOMAIN_QDRANT_PORT="1", AUTOMAIN_QDRANT_TLS="false",
                   AUTOMAIN_QDRANT_COLLECTION_PREFIX=prefix)

        def cli(*args, code=0):
            result = subprocess.run([str(binary), *args, "--json"], cwd=sandbox, env=env,
                                    capture_output=True, text=True, timeout=30)
            assert result.returncode == code, (args, result.stdout, result.stderr)
            for key in (QDRANT_KEY, PROVIDER_KEY, "wrong-key-must-not-be-used"):
                assert key not in result.stdout + result.stderr, "API credential leaked to command output"
            response = json.loads(result.stdout)
            assert response["ok"] == (code == 0), response
            return response["data"] if code == 0 else response["error"]

        status = cli("status")
        assert Path(status["config"]["index_db_path"]) == catalog
        assert status["adapters"]["chunk_catalog"] and status["adapters"]["vector_store"]
        assert status["adapters"]["vector_index"] and status["adapters"]["embedding_provider"]
        assert status["config"]["embedding_model"] == "google/gemini-embedding-2"
        assert status["config"]["embedding_dimensions"] == 3072
        assert not status["config"]["embedding_api_key_set"]
        assert status["index_schema_version"] == 1
        assert not (sandbox / "vectors.db").exists()
        check_env_examples(root, sandbox, binary, env)
        body = "Stale cache: żółć 🐛\n" * 180
        with sqlite3.connect(source) as db:
            for repo, number, kind, title, text in [
                (REPO, 1, "issue", "Cache invalidation", body),
                (REPO, 2, "pull_request", "Invalidate cache entries", "Remove stale entries after updates."),
                (OTHER_REPO, 1, "issue", "Cache invalidation", body),
            ]:
                db.execute("INSERT INTO items(repo,number,kind,state,title,body,author,url,content_hash) VALUES(?,?,?,'open',?,?,?,?,?)",
                           (repo, number, kind, title, text, "source-author",
                            f"https://github.com/{repo}/issues/{number}", hashlib.sha256(text.encode()).hexdigest()))
            db.execute("INSERT INTO comments(repo,comment_id,item_number,author,body,url) VALUES(?,?,1,?,?,?)",
                       (REPO, "101", "comment-author", "I can reproduce this.",
                        f"https://github.com/{REPO}/issues/1#issuecomment-101"))
        prepared = cli("index", "prepare")
        assert prepared["prepared_items"] == 2 and prepared["prepared_chunks"] > 5
        assert prepared["embedded_chunks"] == 0
        first_status = cli("index", "status")
        assert first_status["chunks"] == prepared["prepared_chunks"] and first_status["spaces"] == []
        cli("index", "prepare")
        assert cli("index", "status") == first_status
        with sqlite3.connect(source) as db:
            assert not db.execute("SELECT name FROM sqlite_master WHERE name IN ('chunks','embeddings','embedding_spaces')").fetchall()
        assert_metadata_only(catalog)
        for chunk in catalog_chunks(catalog):
            if chunk["source_kind"] == "item_body" and chunk["item_number"] == 1:
                assert body.encode()[chunk["start_byte"]:chunk["end_byte"]].decode() == chunk["text"]
        for args in [("search", "--query", "cache"), ("index", "embed")]:
            assert cli(*args, code=3)["code"] == "not_configured"
        cli("index", "prepare", "--limit", "1", code=2)
        cli("index", "embed", "--limit", "0", code=2)
        cli("search", "--query", "cache", "--limit", "0", code=2)
        cli("status", "--index-db", str(source), code=2)
        cli("status", "--db", str(catalog), "--index-db", str(sandbox / "other.db"), code=2)
        legacy_unused = sandbox / "not-an-active-index.db"
        env["AUTOMAIN_VECTOR_DB_PATH"] = str(legacy_unused)
        cli("index", "prepare")
        assert not legacy_unused.exists(), "deprecated vector path was opened as an active index"
        del env["AUTOMAIN_VECTOR_DB_PATH"]
        rebuilt = cli("index", "prepare", "--index-db", str(sandbox / "rebuilt.db"))
        assert rebuilt["prepared_chunks"] == prepared["prepared_chunks"]

        with qdrant_container() as (rest, grpc), openrouter_fixture() as (state, base_url):
            env.update(AUTOMAIN_QDRANT_PORT=str(grpc), AUTOMAIN_QDRANT_API_KEY=QDRANT_KEY,
                       OPENROUTER_API_KEY=PROVIDER_KEY, AUTOMAIN_EMBEDDING_BASE_URL=base_url)
            remote = Remote(rest, prefix)
            check_retrieval(sandbox, source, catalog, env, cli, state, remote)
            check_inflight(source, env, cli, state)
            check_validation(sandbox, env, cli, state, remote)
            check_migration(sandbox, catalog, env, cli, state, remote)
            calls = len(state["requests"])
            env["AUTOMAIN_QDRANT_PORT"] = "1"
            del env["OPENROUTER_API_KEY"]
            cli("index", "prepare")
            cli("index", "status")
            cli("status")
            env["OPENROUTER_API_KEY"] = PROVIDER_KEY
            assert cli("index", "reconcile", "--timeout", "2s", code=1)["code"] == "external_unavailable"
            assert cli("similar", "--item", "1", "--timeout", "2s", code=1)["code"] == "external_unavailable"
            assert cli("search", "--query", "cache", "--timeout", "2s", code=1)["code"] == "external_unavailable"
            assert cli("index", "embed", "--timeout", "2s", code=1)["code"] == "external_unavailable"
            assert len(state["requests"]) == calls, "unavailable Qdrant triggered a provider request"
            env["AUTOMAIN_QDRANT_PORT"] = str(grpc)
            check_http(binary, sandbox, env, state)
        assert_metadata_only(catalog)
        with sqlite3.connect(source) as db:
            assert db.execute("SELECT COUNT(*) FROM relationships").fetchone()[0] == 0
            assert db.execute("SELECT COUNT(*) FROM threads").fetchone()[0] == 0
        print("Retrieval checks passed: real authenticated Qdrant 1.20.0, exact cosine parity, repository/space isolation,")
        print("stored-vector similarity, grouped source evidence, recovery/tombstones, migration without provider calls,")
        print("CLI/HTTP wiring, offline preparation, OpenRouter validation, cache reuse, and safe errors.")
        print("Only loopback mock model APIs were used; no live GitHub, OpenRouter, or paid model calls.")


def assert_metadata_only(path):
    with sqlite3.connect(path) as db:
        tables = [row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")]
        assert not set(tables) & {"items", "relationships", "threads", "embeddings"}
        for table in tables:
            columns = db.execute(f'PRAGMA table_info("{table}")').fetchall()
            assert all("BLOB" not in row[2].upper() for row in columns), (table, columns)
            for row in db.execute(f'SELECT * FROM "{table}"'):
                assert not any(isinstance(value, bytes) for value in row), "vector blob persisted in catalog"


def check_env_examples(root, sandbox, binary, env):
    clean_env = {key: value for key, value in env.items() if not key.startswith("AUTOMAIN_")}
    for index, template in enumerate((".env.example", "env.example")):
        directory = sandbox / f"env-example-{index}"
        directory.mkdir()
        (directory / ".env").write_text((root / template).read_text())
        result = subprocess.run([str(binary), "status", "--json"], cwd=directory, env=clean_env,
                                capture_output=True, text=True, timeout=30)
        assert result.returncode == 0, (template, result.stdout, result.stderr)
        response = json.loads(result.stdout)
        assert response["ok"], response
        cfg = response["data"]["config"]
        assert cfg["embedding_provider"] == "openrouter"
        assert cfg["embedding_model"] == "google/gemini-embedding-2"
        assert cfg["embedding_dimensions"] == 3072
        assert cfg["embedding_base_url"] == "https://openrouter.ai/api/v1"
        assert not cfg["embedding_api_key_set"]
        assert cfg["decision_provider"] == "openrouter"
        assert cfg["decision_model"] == "typesafe/jev-1.14"
        assert cfg["decision_endpoint"] == "https://openrouter.ai/api/alpha/decisions"
        assert not cfg["decision_api_key_set"]
        assert Path(cfg["db_path"]) == Path("data/automain.db")
        assert Path(cfg["index_db_path"]) == Path("data/index.db")
        assert cfg["qdrant_host"] == "127.0.0.1" and cfg["qdrant_port"] == 6334
        assert not cfg["qdrant_tls"] and not cfg["qdrant_api_key_set"]
        assert (directory / "data" / "automain.db").is_file()
        assert (directory / "data" / "index.db").is_file()
        assert not (directory / "data" / "vectors.db").exists()


def check_retrieval(sandbox, source, catalog, env, cli, state, remote):
    assert cli("search", "--query", "cache")["matches"] == []
    assert state["requests"] == []
    count = cli("index", "status")["chunks"]
    assert cli("index", "embed", "--limit", "1")["embedded_chunks"] == 1
    assert cli("index", "embed")["embedded_chunks"] == count - 1
    assert all(text.startswith("title: ") and " | text: " in text
               for request in state["requests"] for text in request["input"])
    chunks = catalog_chunks(catalog)
    points = {point["id"]: point for point in remote.points()}
    assert set(points) == {point_id(chunk) for chunk in chunks}, (set(points), {point_id(chunk) for chunk in chunks})
    for chunk in chunks:
        vector = points[point_id(chunk)]["vector"]
        payload = points[point_id(chunk)]["payload"]
        assert all(payload[key] == value for key, value in chunk.items()), payload
        expected = fixture_vector(f"title: {chunk['title']} | text: {chunk['text']}", 3072)
        assert abs(vector[1] / vector[0] - expected[1]) < 1e-6, "response reordering attached vector to wrong passage"
    calls = len(state["requests"])
    assert cli("index", "embed")["embedded_chunks"] == 0
    assert len(state["requests"]) == calls
    matches = cli("search", "--query", "cache", "--limit", "100")["matches"]
    assert len(matches) == count
    assert state["requests"][-1]["input"] == ["task: search result | query: cache"]
    query = fixture_vector("task: search result | query: cache", 3072)
    exact = remote.call("/collections/" + remote.collection() + "/points/query", {
        "query": query, "params": {"exact": True}, "limit": 100, "with_payload": True,
    })["result"]["points"]
    expected = {point["id"]: point["score"] for point in exact}
    assert {point_id(match["chunk"]) for match in matches} == set(expected)
    for match in matches:
        assert abs(match["score"] - expected[point_id(match["chunk"])] ) < 1e-5
        assert abs(match["score"] - cosine(query, points[point_id(match["chunk"])]["vector"])) < 1e-5
    assert all(matches[i]["score"] >= matches[i + 1]["score"] for i in range(len(matches) - 1))

    cli("index", "embed", "--repo", OTHER_REPO)
    assert all(match["chunk"]["repository"] == REPO for match in cli("search", "--query", "cache", "--limit", "100")["matches"])
    source_chunk = next(chunk for chunk in chunks if chunk["item_number"] == 1)
    calls = len(state["requests"])
    del env["OPENROUTER_API_KEY"]
    for args in [("--chunk", source_chunk["id"]), ("--item", "1")]:
        similar = cli("similar", *args, "--limit", "10")
        assert similar["matches"] and {match["item_number"] for match in similar["matches"]} == {2}
        for match in similar["matches"]:
            assert match["passages"]
            for passage in match["passages"]:
                assert passage["chunk"]["repository"] == REPO and passage["chunk"]["item_number"] == 2
                assert passage["chunk"] in chunks or any(
                    all(passage["chunk"].get(key, "") == chunk.get(key, "") for key in chunk) for chunk in chunks)
    assert cli("similar", "--chunk", source_chunk["id"], "--repo", OTHER_REPO, code=1)["code"] == "not_found"
    assert len(state["requests"]) == calls, "stored-ID search called a model provider"
    env["OPENROUTER_API_KEY"] = PROVIDER_KEY

    # Remote loss must invalidate catalog progress, and restored matching points
    # must be reusable without paying for those document embeddings again.
    saved = next(point for point in remote.points() if point["id"] == point_id(source_chunk))
    collection = remote.collection()
    remote.call(f"/collections/{collection}/points/delete?wait=true", {"points": [saved["id"]]})
    calls = len(state["requests"])
    cli("index", "reconcile")
    entry = next(entry for entry in cli("index", "status")["spaces"] if entry["space"]["dimensions"] == 3072)
    assert entry["embedded_chunks"] == count - 1 and entry["pending_chunks"] == 1
    remote.call(f"/collections/{collection}/points?wait=true", {"points": [saved]}, "PUT")
    recovered = cli("index", "embed")
    assert recovered["embedded_chunks"] == 0 and recovered["reused_chunks"] >= 1
    assert len(state["requests"]) == calls
    remote.call(f"/collections/{collection}/points/delete?wait=true", {"points": [saved["id"]]})
    cli("index", "reconcile")
    assert cli("index", "embed")["embedded_chunks"] == 1
    assert len(state["requests"]) == calls + 1

    old_ids = {point_id(chunk) for chunk in chunks if chunk["item_number"] == 1}
    with sqlite3.connect(source) as db:
        db.execute("DELETE FROM comments WHERE repo=?", (REPO,))
        db.execute("UPDATE items SET body='Changed body.' WHERE repo=? AND number=1", (REPO,))
        db.execute("DELETE FROM items WHERE repo=? AND number=2", (REPO,))
    stale = cli("search", "--query", "cache", "--limit", "100")["matches"]
    assert all(match["chunk"]["item_number"] == 1 and match["chunk"]["source_kind"] == "item_title" for match in stale)
    cli("index", "prepare")
    assert cli("index", "status")["pending_deletes"] > 0
    calls = len(state["requests"])
    cli("index", "reconcile")
    assert cli("index", "status")["pending_deletes"] == 0
    assert len(state["requests"]) == calls, "reconcile called an embedding provider"
    current_ids = {point_id(chunk) for chunk in catalog_chunks(catalog)}
    remaining = {point["id"] for point in remote.points()}
    assert not (old_ids - current_ids) & remaining
    assert {point_id(chunk) for chunk in catalog_chunks(catalog, OTHER_REPO)} <= remaining
    assert all(point["payload"]["repository"] != REPO or point["id"] in current_ids for point in remote.points())
    assert cli("index", "embed")["embedded_chunks"] == 1
    assert cli("index", "embed")["embedded_chunks"] == 0

    env["AUTOMAIN_EMBEDDING_DIMENSIONS"] = "128"
    calls = len(state["requests"])
    assert cli("search", "--query", "cache")["matches"] == []
    assert len(state["requests"]) == calls
    assert cli("index", "embed")["embedded_chunks"] == 2
    assert cli("search", "--query", "cache")["space"]["dimensions"] == 128
    assert remote.collection(128) != remote.collection(3072)
    assert len(cli("index", "status")["spaces"]) == 2
    del env["AUTOMAIN_EMBEDDING_DIMENSIONS"]


def check_inflight(source, env, cli, state):
    with sqlite3.connect(source) as db:
        db.execute("UPDATE items SET body='Before in-flight edit.' WHERE repo=? AND number=1", (REPO,))

    def edit_source():
        with sqlite3.connect(source) as db:
            db.execute("UPDATE items SET body='After in-flight edit.' WHERE repo=? AND number=1", (REPO,))

    state["on_request"] = edit_source
    error = cli("index", "embed", code=2)
    assert error["code"] == "invalid_input", error
    assert state["on_request"] is None
    assert cli("index", "status")["pending_deletes"] > 0
    matches = cli("search", "--query", "cache")["matches"]
    assert all(match["chunk"]["source_kind"] != "item_body" for match in matches)
    calls = len(state["requests"])
    cli("index", "reconcile")
    assert len(state["requests"]) == calls
    assert cli("index", "status")["pending_deletes"] == 0
    assert cli("index", "embed")["embedded_chunks"] == 1


def check_validation(sandbox, env, cli, state, remote):
    env["AUTOMAIN_EMBEDDING_API_KEY"] = PROVIDER_KEY
    env["OPENROUTER_API_KEY"] = "wrong-key-must-not-be-used"
    cli("search", "--query", "key precedence")
    del env["AUTOMAIN_EMBEDDING_API_KEY"]
    env["OPENROUTER_API_KEY"] = PROVIDER_KEY
    failed = sandbox / "failed-index.db"
    # Isolate both catalog and collection so remote reuse cannot bypass validation.
    prefix = env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"]
    env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"] = prefix + "_invalid"
    for mode in ["wrong_model", "bad_count", "missing_index", "duplicate_index", "wrong_dimensions", "zero",
                 "null", "huge_number", "malformed", 400, 401, 402, 403, 404, 429, 503, 307]:
        state["mode"] = mode
        calls = len(state["requests"])
        error = cli("index", "embed", "--index-db", str(failed), code=1)
        assert error["code"] == "external_unavailable", error
        assert len(state["requests"]) == calls + 1, "unexpected retry or redirect"
        assert all(entry["embedded_chunks"] == 0 for entry in cli("index", "status", "--index-db", str(failed))["spaces"])
        assert_metadata_only(failed)
    env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"] = prefix
    state["mode"] = "ok"
    incompatible_prefix = prefix + "_incompatible"
    collection = incompatible_prefix + remote.collection()[len(prefix):]
    remote.call("/collections/" + collection, {"vectors": {"size": 3072, "distance": "Dot"}}, "PUT")
    env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"] = incompatible_prefix
    calls = len(state["requests"])
    cli("index", "embed", "--index-db", str(sandbox / "incompatible-index.db"), code=1)
    assert len(state["requests"]) == calls, "incompatible collection reached embedding provider"
    env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"] = prefix
    calls = len(state["requests"])
    cli("search", "--query", "x" * 8100, code=2)
    assert len(state["requests"]) == calls, "oversized input sent to provider"
    for invalid in ["some/other-model", "google/gemini-embedding-2-typo"]:
        env["AUTOMAIN_EMBEDDING_MODEL"] = invalid
        error = cli("status", code=2)
        assert error["code"] == "invalid_input" and "google/gemini-embedding-2" in error["message"]
        cli("index", "embed", code=2)
        cli("search", "--query", "cache", code=2)
    del env["AUTOMAIN_EMBEDDING_MODEL"]
    assert len(state["requests"]) == calls, "unsupported model reached provider"
    for invalid in ["0", "127", "3073", "not-a-number"]:
        env["AUTOMAIN_EMBEDDING_DIMENSIONS"] = invalid
        cli("status", code=2)
    del env["AUTOMAIN_EMBEDDING_DIMENSIONS"]
    base_url = env["AUTOMAIN_EMBEDDING_BASE_URL"]
    env["AUTOMAIN_EMBEDDING_BASE_URL"] = "http://example.com/api/v1"
    cli("status", code=2)
    env["AUTOMAIN_EMBEDDING_BASE_URL"] = base_url
    env["AUTOMAIN_QDRANT_API_KEY"] = "wrong-key-must-not-be-used"
    calls = len(state["requests"])
    cli("index", "reconcile", code=1)
    assert len(state["requests"]) == calls
    env["AUTOMAIN_QDRANT_API_KEY"] = QDRANT_KEY


def check_migration(sandbox, catalog, env, cli, state, remote):
    """Seed the documented legacy schema-v1 format, never a production file."""
    legacy = sandbox / "legacy-vectors.db"
    chunks = catalog_chunks(catalog)
    space = next(entry["space"] for entry in cli("index", "status")["spaces"] if entry["space"]["dimensions"] == 3072)
    space_id = hashlib.sha256(json.dumps(space, separators=(",", ":")).encode()).hexdigest()
    with sqlite3.connect(legacy) as db:
        db.create_function("vec_type", 1, lambda value: "float32", deterministic=True)
        db.create_function("vec_length", 1, lambda value: len(value) // 4, deterministic=True)
        db.executescript("""
            PRAGMA application_id=1096173912;
            CREATE TABLE vector_schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
            INSERT INTO vector_schema_migrations VALUES(1,'2026-10-26T00:00:00Z');
            CREATE TABLE chunks(id TEXT PRIMARY KEY,repo TEXT NOT NULL,item_number INTEGER NOT NULL CHECK(item_number>0),
                item_kind TEXT NOT NULL,source_kind TEXT NOT NULL CHECK(source_kind IN ('item_title','item_body','comment')),
                comment_id TEXT NOT NULL,start_byte INTEGER NOT NULL CHECK(start_byte>=0),end_byte INTEGER NOT NULL CHECK(end_byte>start_byte),
                title TEXT NOT NULL,text TEXT NOT NULL,author TEXT NOT NULL,url TEXT NOT NULL,content_hash TEXT NOT NULL);
            CREATE INDEX chunks_repo_item ON chunks(repo,item_number);
            CREATE TABLE embedding_spaces(id TEXT PRIMARY KEY,provider TEXT NOT NULL,model TEXT NOT NULL,revision TEXT NOT NULL,
                dimensions INTEGER NOT NULL CHECK(dimensions>0),UNIQUE(provider,model,revision,dimensions));
            CREATE TABLE embeddings(chunk_id TEXT NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
                space_id TEXT NOT NULL REFERENCES embedding_spaces(id),content_hash TEXT NOT NULL,
                dimensions INTEGER NOT NULL CHECK(dimensions>0),vector BLOB NOT NULL CHECK(vec_type(vector)='float32' AND vec_length(vector)=dimensions),
                PRIMARY KEY(space_id,chunk_id));
            CREATE INDEX embeddings_chunk ON embeddings(chunk_id);
            CREATE TRIGGER invalidate_changed_chunk AFTER UPDATE OF content_hash ON chunks WHEN OLD.content_hash!=NEW.content_hash
                BEGIN DELETE FROM embeddings WHERE chunk_id=NEW.id; END;
        """)
        db.execute("INSERT INTO embedding_spaces VALUES(?,?,?,?,?)", (space_id, space["provider"], space["model"], space["revision"], 3072))
        for chunk in chunks:
            values = [chunk[key] for key in ("id", "repository", "item_number", "item_kind", "source_kind", "comment_id",
                                            "start_byte", "end_byte", "title", "text", "author", "url", "content_hash")]
            db.execute("INSERT INTO chunks VALUES(" + ",".join("?" for _ in values) + ")", values)
            vector = fixture_vector(f"title: {chunk['title']} | text: {chunk['text']}", 3072)
            db.execute("INSERT INTO embeddings VALUES(?,?,?,?,?)", (chunk["id"], space_id, chunk["content_hash"], 3072, struct.pack("<3072f", *vector)))
    before = hashlib.sha256(legacy.read_bytes()).digest()
    migrated = sandbox / "migrated-index.db"
    prefix = env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"]
    env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"] = prefix + "_migration"
    calls = len(state["requests"])
    del env["OPENROUTER_API_KEY"]
    first = cli("index", "migrate", "--from", str(legacy), "--index-db", str(migrated), "--limit", "1")
    assert first["migrated_chunks"] == 1
    second = cli("index", "migrate", "--from", str(legacy), "--index-db", str(migrated), "--limit", "100")
    assert second["migrated_chunks"] == len(chunks) - 1
    repeated = cli("index", "migrate", "--from", str(legacy), "--index-db", str(migrated), "--limit", "100")
    assert repeated["migrated_chunks"] == 0
    assert len(state["requests"]) == calls
    assert hashlib.sha256(legacy.read_bytes()).digest() == before, "migration modified legacy source"
    migrated_remote = Remote(remote.rest, env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"])
    points = {point["id"]: point for point in migrated_remote.points()}
    assert set(points) == {point_id(chunk) for chunk in chunks}
    for chunk in chunks:
        vector = points[point_id(chunk)]["vector"]
        expected = fixture_vector(f"title: {chunk['title']} | text: {chunk['text']}", 3072)
        assert abs(vector[1] / vector[0] - expected[1]) < 1e-6, "migration changed the legacy vector"
        assert points[point_id(chunk)]["payload"]["content_hash"] == chunk["content_hash"]
    assert cli("index", "status", "--index-db", str(migrated))["spaces"][0]["embedded_chunks"] == len(chunks)
    assert_metadata_only(migrated)
    env["OPENROUTER_API_KEY"] = PROVIDER_KEY
    assert len(cli("search", "--query", "cache", "--index-db", str(migrated))["matches"]) == len(chunks)
    # A healthy server with a dropped collection must not turn stale indexed
    # receipts into a successful empty result. Restore vectors, not paid work.
    collection = migrated_remote.collection()
    config = migrated_remote.call("/collections/" + collection)["result"]["config"]["params"]["vectors"]
    migrated_remote.call("/collections/" + collection, method="DELETE")
    migrated_remote.call("/readyz")
    error = cli("search", "--query", "cache", "--index-db", str(migrated), code=1)
    assert error["code"] == "external_unavailable", error
    assert "collection" in error["message"].lower() and error.get("recovery"), error
    calls = len(state["requests"])
    cli("index", "reconcile", "--index-db", str(migrated))
    entry = cli("index", "status", "--index-db", str(migrated))["spaces"][0]
    assert entry["embedded_chunks"] == 0 and entry["pending_chunks"] == len(chunks)
    migrated_remote.call("/collections/" + collection, {"vectors": config}, "PUT")
    migrated_remote.call("/collections/" + collection + "/points?wait=true", {"points": list(points.values())}, "PUT")
    recovered = cli("index", "embed", "--index-db", str(migrated))
    assert recovered["embedded_chunks"] == 0 and recovered["reused_chunks"] == len(chunks)
    assert len(state["requests"]) == calls, "collection restoration regenerated paid embeddings"
    assert len(cli("search", "--query", "cache", "--index-db", str(migrated))["matches"]) == len(chunks)
    # Collection metadata can survive while every scoped point is lost. Catalog
    # receipts must not make that loss look like a successful empty search.
    scope = {"must": [{"key": "repository", "match": {"value": REPO}},
                      {"key": "space_id", "match": {"value": space_id}}]}
    migrated_remote.call("/collections/" + collection + "/points/delete?wait=true", {"filter": scope})
    migrated_remote.call("/readyz")
    migrated_remote.call("/collections/" + collection)
    assert migrated_remote.call("/collections/" + collection + "/points/count", {
        "filter": scope, "exact": True,
    })["result"]["count"] == 0
    assert cli("index", "status", "--index-db", str(migrated))["spaces"][0]["embedded_chunks"] == len(chunks)
    error = cli("search", "--query", "cache", "--index-db", str(migrated), code=1)
    assert error["code"] == "external_unavailable", error
    assert "reconcile" in error.get("recovery", "").lower() and "restore" in error["recovery"].lower(), error
    calls = len(state["requests"])
    del env["OPENROUTER_API_KEY"]
    cli("index", "reconcile", "--index-db", str(migrated))
    entry = cli("index", "status", "--index-db", str(migrated))["spaces"][0]
    assert entry["embedded_chunks"] == 0 and entry["pending_chunks"] == len(chunks)
    restored = cli("index", "migrate", "--from", str(legacy), "--index-db", str(migrated), "--limit", "100")
    assert restored["migrated_chunks"] == len(chunks)
    assert len(state["requests"]) == calls, "empty-collection recovery regenerated paid embeddings"
    assert hashlib.sha256(legacy.read_bytes()).digest() == before
    env["OPENROUTER_API_KEY"] = PROVIDER_KEY
    assert len(cli("search", "--query", "cache", "--index-db", str(migrated))["matches"]) == len(chunks)

    # Nonempty remote candidates can all be stale after a source edit. That is
    # still a legitimate empty result, distinct from missing remote vectors.
    title = chunks[0]["title"]
    try:
        with sqlite3.connect(env["AUTOMAIN_DB_PATH"]) as db:
            db.execute("UPDATE items SET title=? WHERE repo=? AND number=1", (title + " changed before prepare", REPO))
        assert cli("search", "--query", "cache", "--index-db", str(migrated))["matches"] == []
    finally:
        with sqlite3.connect(env["AUTOMAIN_DB_PATH"]) as db:
            db.execute("UPDATE items SET title=? WHERE repo=? AND number=1", (title, REPO))
    env["AUTOMAIN_QDRANT_COLLECTION_PREFIX"] = prefix


def check_http(binary, sandbox, env, state):
    process = subprocess.Popen([str(binary), "serve", "--listen", "127.0.0.1:0"], cwd=sandbox,
                               env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            assert selector.select(timeout=15), "server did not start"
        line = process.stdout.readline()
        assert line.startswith("Listening on "), line
        url = line.split()[2]
        request_json(url + "/healthz")
        request_json(url + "/readyz")
        calls = len(state["requests"])
        query = urllib.parse.urlencode({"repo": REPO, "item_number": 1, "limit": 10})
        result = request_json(url + "/api/similar?" + query)
        assert result["ok"] and result["data"]["repository"] == REPO and result["data"]["query_passages"] > 0
        result = request_json(url + "/api/similar", {"repository": REPO, "item_number": 1, "limit": 10})
        assert result["ok"]
        assert request_json(url + "/api/index/reconcile", {"repository": REPO})["ok"]
        migrated = request_json(url + "/api/index/migrate", {
            "repository": REPO, "path": str(sandbox / "legacy-vectors.db"), "limit": 100,
        })
        assert migrated["ok"] and migrated["data"]["migrated_chunks"] == 0 and migrated["data"]["reused_chunks"] > 0
        request_json(url + "/api/index/migrate", {"repository": REPO, "path": str(sandbox / "absent.db"), "limit": 100}, status=400)
        assert len(state["requests"]) == calls, "similarity/readiness/reconcile/migrate called a model provider"
    finally:
        process.terminate()
        stdout, stderr = process.communicate(timeout=15)
        assert process.returncode == 0, (stdout, stderr)


if __name__ == "__main__":
    main()
