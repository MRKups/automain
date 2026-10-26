#!/usr/bin/env python3
"""Offline Jev plumbing checks using only a loopback Decisions API fixture."""

import copy
from http.client import HTTPResponse
import http.server
import json
import os
from pathlib import Path
import selectors
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


REQUEST = {
    "state": {"left": {"text": "Stale cache: żółć 🐛"}, "right": {"text": "Invalidate stale entries."}},
    "questions": {
        "related": {"type": "noul", "instructions": "Do `left` and `right` concern the same concrete problem?"},
        "kind": {
            "type": "choice",
            "instructions": {"question": "How does `right` relate to `left`?", "focus": "Use only the supplied text."},
            "criteria": {"fix": {"what": "Proposes a fix", "examples": ["Invalidate stale data"]}, "other": None},
        },
        "strength": {
            "type": "score", "instructions": "How directly does `right` address `left`?",
            "criteria": ["Different problems", {"what": "Same subject, no direct solution"}, "Direct solution"],
        },
    },
}


def fixture_response(request):
    answers = {}
    for key, question in request["questions"].items():
        kind = question["type"]
        if kind == "noul":
            answer = {"type": kind, "noul": 0.0}  # Zero must not become missing.
        elif kind == "choice":
            options = list(question["criteria"])
            answer = {"type": kind, "choice": options[0], "confidence": 1.0,
                      "probabilities": {option: float(i == 0) for i, option in enumerate(options)}}
        else:
            levels = question["criteria"]
            answer = {"type": kind, "score": 1.43, "confidence": 0.35,
                      "probabilities": {"0": 0.0, "1": 0.57, "2": 0.43},
                      "legend": {str(i): level for i, level in enumerate(levels)}}
        answers[key] = answer
    return {"id": "fixture-decision", "model": "typesafe/jev-1.14-20261014", "provider": "TypeSafe",
            "answers": answers, "usage": {"input_tokens": 476, "output_tokens": 70, "cost": 0.0}}


def main():
    root = Path(__file__).resolve().parents[1]
    temporary_root = Path(os.environ.get("TMPDIR", tempfile.gettempdir()))
    if (temporary_root / "opencode").is_dir():
        temporary_root /= "opencode"
    with tempfile.TemporaryDirectory(prefix="automain-decisions-", dir=temporary_root) as directory:
        sandbox = Path(directory)
        binary = sandbox / "automain"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/automain"], cwd=root, check=True)
        env = {key: value for key, value in os.environ.items()
               if not key.startswith(("AUTOMAIN_", "GITHUB_", "OPENAI_", "GEMINI_", "DEEPSEEK_", "OPENROUTER_", "TYPESAFE_"))}
        env.update(AUTOMAIN_DB_PATH=str(sandbox / "source.db"),
                   AUTOMAIN_INDEX_DB_PATH=str(sandbox / "index.db"),
                   AUTOMAIN_QDRANT_HOST="127.0.0.1", AUTOMAIN_QDRANT_PORT="1",
                   AUTOMAIN_QDRANT_TLS="false")
        request_path = sandbox / "request.json"
        request_path.write_text(json.dumps(REQUEST, ensure_ascii=False))
        state = {"requests": [], "mode": "ok", "mutation": None}

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                state["requests"].append((self.path, dict(self.headers), request))
                assert self.path == "/api/alpha/decisions", self.path
                assert "messages" not in request and set(request) == {"model", "state", "questions"}
                mode = state["mode"]
                if isinstance(mode, int):
                    self.send_response(mode)
                    if mode == 307:
                        self.send_header("Location", "/unexpected-redirect")
                    self.end_headers()
                    self.wfile.write(b'provider-secret: fixture-dedicated-key; private source text')
                    return
                response = fixture_response(request)
                if state["mutation"]:
                    state["mutation"](response)
                payload = json.dumps(response).encode()
                if mode == "malformed":
                    payload = b'{provider-secret: fixture-dedicated-key'
                elif mode == "duplicate":
                    payload = payload.replace(b'"noul": 0.0', b'"noul": 0.0, "noul": 1.0')
                elif mode == "oversized":
                    payload = b" " * (1048576 + 1)
                elif mode == "trailing":
                    payload += b" {}"
                elif mode == "slow":
                    time.sleep(0.8)
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                try:
                    self.wfile.write(payload)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        mock = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        worker = threading.Thread(target=mock.serve_forever, daemon=True)
        worker.start()
        env["AUTOMAIN_DECISION_ENDPOINT"] = f"http://127.0.0.1:{mock.server_port}/api/alpha/decisions"
        env["AUTOMAIN_EMBEDDING_BASE_URL"] = f"http://127.0.0.1:{mock.server_port}/api/v1"
        env["AUTOMAIN_LLM_BASE_URL"] = f"http://127.0.0.1:{mock.server_port}/api/v1"

        def cli(*args, code=0):
            result = subprocess.run([str(binary), *args, "--json"], cwd=sandbox, env=env,
                                    capture_output=True, text=True, timeout=30)
            assert result.returncode == code, (args, result.stdout, result.stderr)
            assert "fixture-shared-key" not in result.stdout + result.stderr
            assert "fixture-dedicated-key" not in result.stdout + result.stderr
            assert "provider-secret" not in result.stdout + result.stderr
            response = json.loads(result.stdout)
            assert response["ok"] == (code == 0), response
            return response["data"] if code == 0 else response["error"]

        def decide(code=0, *args):
            return cli("decide", "--input", str(request_path), *args, code=code)

        try:
            status = cli("status")
            assert status["adapters"]["decision_provider"]
            assert status["config"]["decision_model"] == "typesafe/jev-1.14"
            assert status["config"]["decision_provider"] == "openrouter"
            assert not status["config"]["decision_api_key_set"]
            assert not state["requests"], "status made a model request"
            assert decide(3)["code"] == "not_configured"
            env["AUTOMAIN_LLM_API_KEY"] = "unused-legacy-key"
            env["AUTOMAIN_EMBEDDING_API_KEY"] = "unused-embedding-key"
            assert decide(3)["code"] == "not_configured"
            assert not state["requests"], "unconfigured request reached provider"

            env["OPENROUTER_API_KEY"] = "fixture-shared-key"
            result = decide()
            assert result["model"] == "typesafe/jev-1.14-20261014"
            assert result["usage"]["cost"] == 0.0
            assert result["answers"]["related"] == {"type": "noul", "noul": 0}
            assert "confidence" not in result["answers"]["related"]
            path, headers, sent = state["requests"][-1]
            assert headers["Authorization"] == "Bearer fixture-shared-key"
            assert sent["state"] == REQUEST["state"] and sent["questions"] == REQUEST["questions"]
            assert sent["model"] == "typesafe/jev-1.14"
            env["AUTOMAIN_DECISION_API_KEY"] = "fixture-dedicated-key"
            decide()
            assert state["requests"][-1][1]["Authorization"] == "Bearer fixture-dedicated-key"

            request_path.write_bytes((root / "internal/decision/example.json").read_bytes())
            assert set(decide()["answers"]) == {"same_problem", "candidate_role", "solution_coverage"}
            request_path.write_text(json.dumps(REQUEST))
            human = subprocess.run([str(binary), "decide", "--input", str(request_path)], cwd=sandbox,
                                   env=env, capture_output=True, text=True, timeout=30)
            assert human.returncode == 0, human.stderr
            assert json.loads(human.stdout)["answers"]["related"]["noul"] == 0

            # Optional metadata/cost and additional response fields remain compatible.
            def optional_response(response):
                response.pop("id")
                response.pop("provider")
                response["usage"].pop("cost")
                response["new_metadata"] = {"future": True}

            state["mutation"] = optional_response
            assert "cost" not in decide()["usage"]
            state["mutation"] = lambda r: r.update(model="typesafe/jev-1.14")
            assert decide()["model"] == "typesafe/jev-1.14"
            env["AUTOMAIN_DECISION_MODEL"] = "typesafe/jev-1.13"
            state["mutation"] = lambda r: r.update(model="typesafe/jev-1.13-20260917")
            assert decide()["model"] == "typesafe/jev-1.13-20260917"
            del env["AUTOMAIN_DECISION_MODEL"]
            state["mutation"] = None

            # Optional true/false criteria and structured/array state are preserved.
            variant = copy.deepcopy(REQUEST)
            variant["state"] = [REQUEST["state"], "Additional text"]
            variant["questions"]["related"]["criteria"] = {"true": "Same concrete problem", "false": {"what": "Different problems"}}
            request_path.write_text(json.dumps(variant))
            decide()
            assert state["requests"][-1][2]["questions"] == variant["questions"]

            calls = len(state["requests"])
            invalid_inputs = [
                {}, {"state": None, "questions": REQUEST["questions"]},
                {"state": 42, "questions": REQUEST["questions"]},
                {"state": "text", "questions": {}},
                {**REQUEST, "model": "some/chat-model"},
                {**REQUEST, "api_key": "not-allowed"},
                {**REQUEST, "state": "x" * 30001},
                {**REQUEST, "state": {"n": float("inf")}},
            ]
            for change in [
                {"type": "box", "instructions": "Find image"},
                {"type": "noul"}, {"type": "noul", "instructions": ""},
                {"type": "noul", "instructions": "Question?", "criteria": {"true": "Yes"}},
                {"type": "choice", "instructions": "Which?", "criteria": {"one": "Only option"}},
                {"type": "choice", "instructions": "Which?", "criteria": {str(i): None for i in range(256)}},
                {"type": "score", "instructions": "How?", "criteria": ["Only level"]},
                {"type": "score", "instructions": "How?", "criteria": ["level"] * 11},
                {"type": "score", "instructions": "How?", "criteria": [0, 1, 2]},
                {"type": "noul", "instructions": "Question?", "unknown": "field"},
            ]:
                invalid_inputs.append({"state": "text", "questions": {"test": change}})
            for value in invalid_inputs:
                request_path.write_text(json.dumps(value))
                assert decide(2)["code"] == "invalid_input"
            for raw in [b"", b"{} {}", b'\xff', b'{"state":"a","state":"b","questions":{}}',
                        b'{"state":' + b"[" * 65 + b"0" + b"]" * 65 + b',"questions":{}}',
                        b'{"state":{"n":1e400},"questions":{}}']:
                request_path.write_bytes(raw)
                decide(2)
            cli("decide", code=2)
            cli("decide", "--input", str(sandbox / "missing.json"), code=2)
            cli("decide", "--input", str(sandbox), code=2)
            assert len(state["requests"]) == calls, "invalid input reached provider"
            request_path.write_text(json.dumps(REQUEST))

            original_endpoint = env["AUTOMAIN_DECISION_ENDPOINT"]
            for endpoint in ["http://example.com/api/alpha/decisions", "https://key:secret@example.com/decisions",
                             "https://example.com/decisions?key=secret", "https://example.com/decisions#fragment",
                             "https://example.com", "relative/path"]:
                env["AUTOMAIN_DECISION_ENDPOINT"] = endpoint
                cli("status", code=2)
            env["AUTOMAIN_DECISION_ENDPOINT"] = original_endpoint
            env["AUTOMAIN_DECISION_MODEL"] = "openai/chat-model"
            decide(2)
            assert len(state["requests"]) == calls
            env["AUTOMAIN_DECISION_MODEL"] = "~typesafe/jev-latest"
            decide()
            assert state["requests"][-1][2]["model"] == "~typesafe/jev-latest"
            del env["AUTOMAIN_DECISION_MODEL"]

            mutations = [
                lambda r: r.update(model="some/fallback-model"),
                lambda r: r.update(error={"message": "provider-secret"}),
                lambda r: r["answers"].pop("related"),
                lambda r: r["answers"].update(extra={"type": "noul", "noul": 1}),
                lambda r: r["answers"]["related"].update(type="choice"),
                lambda r: r["answers"]["related"].update(noul=None),
                lambda r: r["answers"]["related"].update(noul=1.01),
                lambda r: r["answers"]["related"].update(noul=float("nan")),
                lambda r: r["answers"]["related"].update(confidence=1),
                lambda r: r["answers"]["kind"].update(choice="unknown"),
                lambda r: r["answers"]["kind"].update(choice="other"),
                lambda r: r["answers"]["kind"].pop("confidence"),
                lambda r: r["answers"]["kind"].update(confidence=2),
                lambda r: r["answers"]["kind"].update(probabilities={"fix": 0.2, "other": 0.2}),
                lambda r: r["answers"]["kind"].update(probabilities={"fix": 1, "other": None}),
                lambda r: r["answers"]["kind"].update(probabilities={"fix": 1, "wrong": 0}),
                lambda r: r["answers"]["strength"].update(score=0),
                lambda r: r["answers"]["strength"].update(score=3),
                lambda r: r["answers"]["strength"].update(legend={"0": "Invented level"}),
                lambda r: r["usage"].pop("input_tokens"),
                lambda r: r["usage"].update(output_tokens=-1),
                lambda r: r["usage"].update(cost=-1),
            ]
            for mutation in mutations:
                state["mutation"] = mutation
                calls = len(state["requests"])
                assert decide(1)["code"] == "external_unavailable"
                assert len(state["requests"]) == calls + 1, "unexpected retry"
            state["mutation"] = None
            for mode in [400, 401, 402, 403, 404, 413, 422, 429, 500, 529, 307,
                         "malformed", "duplicate", "oversized", "trailing"]:
                state["mode"] = mode
                calls = len(state["requests"])
                decide(1)
                assert len(state["requests"]) == calls + 1, "unexpected retry/redirect"
            state["mode"] = "slow"
            calls = len(state["requests"])
            assert decide(1, "--timeout", "200ms")["code"] == "external_unavailable"
            assert len(state["requests"]) == calls + 1
            state["mode"] = "ok"

            check_http(binary, sandbox, env, state)
            for name, tables in [("source.db", ("items", "comments", "threads", "relationships")),
                                 ("index.db", ("chunks", "embedding_spaces", "index_entries"))]:
                with sqlite3.connect(sandbox / name) as db:
                    for table in tables:
                         assert db.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0] == 0, "decision mutated storage"
            assert not (sandbox / "vectors.db").exists(), "decision initialized a deprecated vector file"
        finally:
            mock.shutdown()
            mock.server_close()
            worker.join(timeout=5)
        print("Decision checks passed: Jev wire format, typed answers, probabilities, configuration,")
        print("CLI/HTTP wiring, input/response validation, safe failures, provider/body deadlines, and no storage writes.")
        print("Only a local mock Decisions API was used; no live or paid model calls.")


def check_http(binary, sandbox, env, state):
    process = subprocess.Popen([str(binary), "serve", "--listen", "127.0.0.1:0", "--timeout", "500ms"], cwd=sandbox,
                               env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            assert selector.select(timeout=15), "server did not start"
        line = process.stdout.readline()
        assert line.startswith("Listening on "), line
        url = line.split()[2]

        def http(path="/api/decide", payload=None, method="POST", content_type="application/json", status=200):
            request = urllib.request.Request(url + path, data=payload, method=method,
                                             headers={"Content-Type": content_type})
            try:
                response = urllib.request.urlopen(request, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                data = response.read().decode()
                assert response.status == status, (response.status, data)
                assert "fixture-dedicated-key" not in data and "provider-secret" not in data
                return json.loads(data)

        payload = json.dumps(REQUEST).encode()
        assert http("/healthz", method="GET")["ok"]
        assert not http("/readyz", method="GET", status=503)["ok"]
        assert http(payload=payload)["data"]["answers"]["strength"]["score"] == 1.43
        assert http("/api/v1/decide", payload=payload)["ok"]
        calls = len(state["requests"])
        http(payload=payload, method="GET", status=405)
        http(payload=payload, content_type="text/plain", status=400)
        http(payload=b"{} {}", status=400)
        http(payload=b"x" * 30001, status=400)
        http(payload=json.dumps({**REQUEST, "model": "override"}).encode(), status=400)
        assert len(state["requests"]) == calls
        address = urllib.parse.urlsplit(url)
        with socket.create_connection((address.hostname, address.port), timeout=2) as connection:
            connection.settimeout(2)
            started = time.monotonic()
            connection.sendall((f"POST /api/decide HTTP/1.1\r\nHost: {address.netloc}\r\n"
                                "Content-Type: application/json\r\nContent-Length: 1000\r\n"
                                "Connection: close\r\n\r\n{").encode())
            # No EOF: the handler must stop waiting for the rest of this body.
            response = HTTPResponse(connection)
            response.begin()
            with response:
                result = json.loads(response.read())
                assert response.status == 502, (response.status, result)
                assert not result["ok"] and result["error"]["code"] == "external_unavailable", result
            assert time.monotonic() - started < 2, "partial decision body outlived the request deadline"
        assert len(state["requests"]) == calls, "partial decision body reached the provider"
        state["mode"] = 429
        assert http(payload=payload, status=502)["error"]["code"] == "external_unavailable"
        state["mode"] = "slow"
        calls = len(state["requests"])
        started = time.monotonic()
        assert http(payload=payload, status=502)["error"]["code"] == "external_unavailable"
        assert time.monotonic() - started < 2, "provider deadline did not bound HTTP decisions"
        assert len(state["requests"]) == calls + 1, "HTTP decision timeout retried or bypassed the provider"
        state["mode"] = "ok"
        assert http(payload=payload)["ok"]
    finally:
        process.terminate()
        stdout, stderr = process.communicate(timeout=15)
        assert process.returncode == 0, (stdout, stderr)


if __name__ == "__main__":
    main()
