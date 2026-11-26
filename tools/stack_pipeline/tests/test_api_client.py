"""Adapter and CLI tests against a real loopback HTTP server (no mocks of urllib)."""

import io
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

import pytest

from tools.stack_pipeline.api_client import ApiError, Credential, IacpApiClient, TokenSource
from tools.stack_pipeline.cli import main

SHA = "e4" * 20
STACK = "11111111-1111-4111-8111-111111111111"
REQ = "22222222-2222-4222-8222-222222222222"
INTERNAL = "internal-token-0123456789abcdef0123456789"


class FakeApi:
    """Records requests; ``routes[(method, path)]`` is a list of (status, body, headers) replies."""

    def __init__(self):
        self.calls = []
        self.routes = {}

    def reply(self, method, path, status, body=None, headers=None):
        self.routes.setdefault((method, path), []).append((status, body, headers or {}))

    def handler(self):
        api = self

        class Handler(BaseHTTPRequestHandler):
            def _serve(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                parts = urlsplit(self.path)
                api.calls.append({
                    "method": self.command,
                    "path": parts.path,
                    "query": parse_qs(parts.query),
                    "headers": dict(self.headers),
                    "body": json.loads(raw) if raw else None,
                })
                replies = api.routes.get((self.command, parts.path)) or [(404, {"code": "not_found"}, {})]
                status, body, headers = replies.pop(0) if len(replies) > 1 else replies[0]
                data = json.dumps(body).encode() if body is not None else b""
                self.send_response(status)
                for k, v in headers.items():
                    self.send_header(k, v)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            do_GET = do_POST = do_DELETE = _serve

            def log_message(self, *args):  # keep pytest output clean
                pass

        return Handler


@pytest.fixture
def api():
    fake = FakeApi()
    server = ThreadingHTTPServer(("127.0.0.1", 0), fake.handler())
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    fake.url = f"http://127.0.0.1:{server.server_address[1]}"
    yield fake
    server.shutdown()
    server.server_close()


def client(api, **kw):
    return IacpApiClient(api.url, Credential(INTERNAL, "internal"), sleep=lambda _: None, **kw)


LOCK = {"stack_id": STACK, "request_id": REQ, "holder": "gha", "fencing_token": 48214,
        "acquired_at": "2026-11-26T00:00:00Z", "expires_at": "2026-12-03T00:00:00Z"}


class TestIacpApiClient:
    def test_post_run(self, api):
        api.reply("POST", "/v1/runs", 200, {"accepted": True, "run_id": "r1", "run_status": "succeeded"})
        resp = client(api).post_run({"pipeline": "validate"})
        assert resp.body["run_status"] == "succeeded"
        call = api.calls[0]
        assert call["headers"]["Authorization"] == f"Bearer {INTERNAL}"
        assert call["headers"]["Content-Type"] == "application/json"
        assert call["body"] == {"pipeline": "validate"}

    def test_refuses_redirects(self, api):
        api.reply("POST", "/v1/runs", 307, None, {"Location": "http://evil.example/v1/runs"})
        with pytest.raises(ApiError, match="redirect .* refused") as exc:
            client(api).post_run({})
        assert exc.value.status == 307
        assert len(api.calls) == 1  # not replayed anywhere

    def test_retries_server_errors(self, api):
        api.reply("POST", "/v1/runs", 503, {"code": "unavailable"})
        api.reply("POST", "/v1/runs", 200, {"accepted": True})
        assert client(api).post_run({}).status == 200
        assert len(api.calls) == 2

    def test_problem_is_surfaced_and_not_retried(self, api):
        api.reply("POST", "/v1/runs", 409, {"code": "stale_fencing_token", "title": "Conflict"})
        with pytest.raises(ApiError, match="stale_fencing_token") as exc:
            client(api).post_run({})
        assert exc.value.code == "stale_fencing_token"
        assert len(api.calls) == 1

    def test_acquire_lock(self, api):
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "granted", "queued_behind": None, "current": LOCK})
        claim = client(api).acquire_lock(STACK, REQ.upper())
        assert (claim.outcome, claim.fencing_token, claim.request_id) == ("granted", 48214, REQ)
        assert api.calls[0]["body"] == {"request_id": REQ}

    def test_acquire_queued_raises(self, api):
        other = {**LOCK, "request_id": "33333333-3333-4333-8333-333333333333"}
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 202,
                  {"lock": "queued", "queued_behind": other["request_id"], "current": other})
        with pytest.raises(ApiError, match="not granted: queued") as exc:
            client(api).acquire_lock(STACK, REQ)
        assert exc.value.code == "stack_locked"

    def test_acquire_for_another_request_raises(self, api):
        other = {**LOCK, "request_id": "33333333-3333-4333-8333-333333333333"}
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "held", "current": other})
        with pytest.raises(ApiError, match="another request"):
            client(api).acquire_lock(STACK, REQ)

    def test_release_lock(self, api):
        api.reply("DELETE", f"/v1/internal/stacks/{STACK}/lock", 200, {"released": LOCK, "promoted": None})
        client(api).release_lock(STACK, REQ, 48214)
        assert api.calls[0]["query"] == {"request_id": [REQ], "fencing_token": ["48214"]}
        assert api.calls[0]["body"] is None

    def test_release_is_not_retried(self, api):
        api.reply("DELETE", f"/v1/internal/stacks/{STACK}/lock", 503, {})
        with pytest.raises(ApiError):
            client(api).release_lock(STACK, REQ, 1)
        assert len(api.calls) == 1

    def test_ids_must_be_uuids(self, api):
        with pytest.raises(ValueError, match="stack id"):
            client(api).acquire_lock("../../runs", REQ)

    def test_plain_http_only_on_loopback(self):
        with pytest.raises(ValueError):
            IacpApiClient("http://api.example.com", Credential("t", "internal"))


class TestTokenSource:
    def test_oidc_token_for_audience(self, api):
        api.reply("GET", "/token", 200, {"value": "oidc-jwt"})
        out = io.StringIO()
        env = {"ACTIONS_ID_TOKEN_REQUEST_URL": f"{api.url}/token?api-version=2.0",
               "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "req-token", "IACP_INTERNAL_TOKEN": INTERNAL, "GITHUB_ACTIONS": "true"}
        cred = TokenSource(env, stdout=out).credential("https://api.iacp.example")
        assert (cred.token, cred.kind) == ("oidc-jwt", "oidc")
        assert api.calls[0]["query"] == {"api-version": ["2.0"], "audience": ["https://api.iacp.example"]}
        assert api.calls[0]["headers"]["Authorization"] == "bearer req-token"
        assert out.getvalue() == "::add-mask::oidc-jwt\n"
        assert "oidc-jwt" not in repr(cred)

    def test_falls_back_to_internal_token_without_oidc(self):
        out = io.StringIO()
        cred = TokenSource({"IACP_INTERNAL_TOKEN": INTERNAL, "GITHUB_ACTIONS": "true"}, stdout=out).credential("aud")
        assert cred.kind == "internal"
        assert out.getvalue() == f"::add-mask::{INTERNAL}\n"

    def test_oidc_without_audience_never_falls_back(self, api):
        # A job that can mint OIDC tokens must not run unscoped with the shared token.
        env = {"ACTIONS_ID_TOKEN_REQUEST_URL": f"{api.url}/token", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "x",
               "IACP_INTERNAL_TOKEN": INTERNAL}
        out = io.StringIO()
        with pytest.raises(ApiError, match="no audience"):
            TokenSource({**env, "GITHUB_ACTIONS": "true"}, stdout=out).credential("  ")
        assert api.calls == []
        assert out.getvalue() == ""  # nothing masked, nothing chosen

    def test_failed_oidc_request_does_not_fall_back(self, api):
        api.reply("GET", "/token", 403, {})
        env = {"ACTIONS_ID_TOKEN_REQUEST_URL": f"{api.url}/token", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "x",
               "IACP_INTERNAL_TOKEN": INTERNAL}
        with pytest.raises(ApiError, match="HTTP 403"):
            TokenSource(env).credential("aud")

    def test_nothing_available(self):
        assert TokenSource({}).credential("aud") is None


# ---- CLI ----------------------------------------------------------------------------


@pytest.fixture
def runner_env(monkeypatch):
    for k in ("ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "IACP_API_URL", "IACP_OIDC_AUDIENCE",
              "GITHUB_ACTIONS", "GITHUB_OUTPUT"):
        monkeypatch.delenv(k, raising=False)
    monkeypatch.setenv("GITHUB_RUN_ID", "42")
    monkeypatch.setenv("GITHUB_RUN_ATTEMPT", "1")
    monkeypatch.setenv("GITHUB_SHA", SHA)
    monkeypatch.setenv("GITHUB_SERVER_URL", "https://github.com")
    monkeypatch.setenv("GITHUB_REPOSITORY", "acme/repo")
    monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
    return monkeypatch


def write_summary(tmp_path, ok=True):
    path = tmp_path / "summary.json"
    path.write_text(json.dumps({"ok": ok, "plan_hash": "ab" * 32, "counts": {"create": 1},
                                "policy": {"failures": [], "warnings": []},
                                "changes": [{"address": "aws_s3_bucket.b", "action": "create"}]}))
    return path


class TestReportCommand:
    def test_validate_report_reads_the_stack_file(self, tmp_path, make_stack, runner_env, api, capsys):
        root = make_stack("development", "platform", "smoke", fencing_token=7, request_id="req-smoke1")
        api.reply("POST", "/v1/runs", 200, {"accepted": True, "run_id": "r1", "run_status": "succeeded",
                                            "request_status": "plan_ready"})
        rc = main(["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "validate", "--stage", "summary",
                   "--summary", str(write_summary(tmp_path)), "--api-url", api.url])
        assert rc == 0
        body = api.calls[0]["body"]
        assert body["stack_path"] == "environments/development/platform/smoke"
        assert (body["request_short_id"], body["fencing_token"], body["status"]) == ("req-smoke1", 7, "passed")
        assert body["github"] == {"run_id": 42, "run_attempt": 1, "head_sha": SHA}
        assert body["plan"]["counts"]["add"] == 1
        assert "run r1 is succeeded" in capsys.readouterr().out

    def test_missing_summary_reports_failed(self, tmp_path, make_stack, runner_env, capsys):
        root = make_stack("development", "platform", "smoke")
        rc = main(["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "validate", "--stage", "summary",
                   "--summary", str(tmp_path / "missing.json"), "--dry-run"])
        assert rc == 0
        payload = json.loads(capsys.readouterr().out)
        assert payload["status"] == "failed" and "plan" not in payload

    def test_apply_does_not_borrow_the_stack_files_token(self, tmp_path, make_stack, runner_env, capsys):
        root = make_stack("development", "platform", "smoke")
        args = ["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "apply", "--stage", "plan",
                "--status", "success", "--dry-run"]
        assert main(args) == 1
        assert "/fencing_token" in capsys.readouterr().err
        assert main([*args, "--fencing-token", "9"]) == 0

    def test_outputs_are_redacted(self, tmp_path, make_stack, runner_env, capsys):
        root = make_stack("development", "platform", "smoke")
        outputs = tmp_path / "outputs.json"
        outputs.write_text(json.dumps({"a": {"sensitive": False, "value": 1}, "b": {"sensitive": True, "value": "s3cr3t"}}))
        main(["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "apply", "--stage", "summary",
              "--status", "passed", "--fencing-token", "9", "--outputs", str(outputs), "--terraform-exit-code", "0",
              "--dry-run"])
        payload = json.loads(capsys.readouterr().out)
        assert payload["outputs"] == {"a": 1, "b": "<sensitive>"}
        assert "s3cr3t" not in json.dumps(payload)

    def test_skips_when_unconfigured(self, tmp_path, make_stack, runner_env, capsys):
        root = make_stack("development", "platform", "smoke")
        rc = main(["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "validate", "--stage", "summary",
                   "--status", "passed", "--skip-if-unconfigured"])
        assert rc == 0
        assert capsys.readouterr().out.startswith("::notice title=Platform API not configured::")

    def test_warn_on_error(self, tmp_path, make_stack, runner_env, api, capsys):
        root = make_stack("development", "platform", "smoke")
        api.reply("POST", "/v1/runs", 404, {"code": "not_found", "title": "Not Found"})
        args = ["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "validate", "--stage", "summary",
                "--status", "passed", "--api-url", api.url]
        assert main([*args, "--warn-on-error"]) == 0
        out = capsys.readouterr().out
        assert "::warning title=Run report not sent::" in out and INTERNAL not in out
        assert main(args) == 1

    def test_no_credential(self, tmp_path, make_stack, runner_env, api, capsys):
        runner_env.delenv("IACP_INTERNAL_TOKEN")
        root = make_stack("development", "platform", "smoke")
        rc = main(["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "validate", "--stage", "summary",
                   "--status", "passed", "--api-url", api.url])
        assert rc == 1
        assert "no credential" in capsys.readouterr().err
        assert api.calls == []


class TestLockCommand:
    def test_acquire_writes_outputs(self, tmp_path, runner_env, api):
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "granted", "current": LOCK})
        out = tmp_path / "out"
        rc = main(["lock", "acquire", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url,
                   "--github-output", str(out)])
        assert rc == 0
        assert out.read_text() == "claimed=true\nfencing_token=48214\n"

    def test_unconfigured_skip(self, tmp_path, runner_env, capsys):
        out = tmp_path / "out"
        rc = main(["lock", "acquire", "--stack-id", STACK, "--request-id", REQ, "--skip-if-unconfigured",
                   "--github-output", str(out)])
        assert rc == 0
        assert out.read_text() == "claimed=false\n"
        assert "::notice" in capsys.readouterr().out

    def test_release_needs_token(self, runner_env, api, capsys):
        rc = main(["lock", "release", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url])
        assert rc == 2
        assert "--fencing-token" in capsys.readouterr().err

    def test_queued_fails(self, runner_env, api, capsys):
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 202, {"lock": "queued", "queued_behind": REQ, "current": LOCK})
        assert main(["lock", "acquire", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url]) == 1
        assert "not granted" in capsys.readouterr().err
