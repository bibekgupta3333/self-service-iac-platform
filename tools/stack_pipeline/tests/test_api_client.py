"""Adapter and CLI tests against a real loopback HTTP server (no mocks of urllib)."""

import io
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

import pytest

from tools.stack_pipeline.api_client import ApiError, Credential, FreshCredentials, IacpApiClient, TokenSource
from tools.stack_pipeline.cli import annotation, main

SHA = "e4" * 20
STACK = "11111111-1111-4111-8111-111111111111"
REQ = "22222222-2222-4222-8222-222222222222"
INTERNAL = "internal-token-0123456789abcdef0123456789"


class FakeApi:
    """Records requests; ``routes[(method, path)]`` is a list of (status, body, headers) replies."""

    def __init__(self):
        self.calls = []
        self.routes = {}
        self.delay = 0.0  # seconds to stall before answering (timeouts)

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
                if api.delay:
                    time.sleep(api.delay)
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


class QuietServer(ThreadingHTTPServer):
    def handle_error(self, request, client_address):  # a timed-out client hangs up mid-reply
        pass


@pytest.fixture
def api():
    fake = FakeApi()
    server = QuietServer(("127.0.0.1", 0), fake.handler())
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


BY_PATH = "/v1/internal/requests/by-path"
STACK_PATH = "environments/development/payments/orders"
HASH = "ab" * 32
RESOLVED = {"stack_id": STACK, "stack_path": STACK_PATH, "request_id": REQ, "request_short_id": "req-7k3p9x",
            "kind": "update", "status": "approved", "fencing_token": 48214, "approved_plan_hash": HASH}


class TestResolveRequest:
    def test_resolves(self, api):
        api.reply("GET", BY_PATH, 200, RESOLVED)
        got = client(api).resolve_request(STACK_PATH)
        assert (got.stack_id, got.request_id, got.fencing_token, got.approved_plan_hash) == (STACK, REQ, 48214, HASH)
        assert api.calls[0]["query"] == {"stack_path": [STACK_PATH]}
        assert api.calls[0]["headers"]["Authorization"] == f"Bearer {INTERNAL}"

    def test_a_missing_hash_is_empty(self, api):
        api.reply("GET", BY_PATH, 200, {k: v for k, v in RESOLVED.items() if k != "approved_plan_hash"})
        assert client(api).resolve_request(STACK_PATH).approved_plan_hash == ""

    @pytest.mark.parametrize("path", ["", "environments/development/payments", "environments/dev/payments/orders",
                                      STACK_PATH + "/", STACK_PATH + "\n::error::x", "../" + STACK_PATH])
    def test_rejects_a_bad_path_before_calling(self, api, path):
        with pytest.raises(ValueError, match="stack path"):
            client(api).resolve_request(path)
        assert api.calls == []

    def test_rejects_a_bad_head_sha(self, api):
        with pytest.raises(ValueError, match="head sha"):
            client(api).resolve_request(STACK_PATH, "abc")
        assert api.calls == []

    @pytest.mark.parametrize("patch", [
        {"stack_id": "nope"}, {"request_id": ""}, {"fencing_token": 0}, {"fencing_token": True}, {"fencing_token": "7"},
        {"approved_plan_hash": "xyz"},
    ])
    def test_rejects_a_malformed_answer(self, api, patch):
        api.reply("GET", BY_PATH, 200, {**RESOLVED, **patch})
        with pytest.raises(ApiError):
            client(api).resolve_request(STACK_PATH)

    def test_merge_not_recorded_is_retried_then_raised(self, api):
        api.reply("GET", BY_PATH, 409, {"code": "merge_not_recorded"})
        naps = []
        c = IacpApiClient(api.url, Credential(INTERNAL, "internal"), sleep=naps.append)
        with pytest.raises(ApiError, match="merge_not_recorded") as exc:
            c.resolve_request(STACK_PATH, merge_wait_attempts=3, merge_wait_seconds=1.5)
        assert exc.value.status == 409 and len(api.calls) == 3 and naps == [1.5, 1.5]

    def test_merge_not_recorded_is_retried_until_recorded(self, api):
        api.reply("GET", BY_PATH, 409, {"code": "merge_not_recorded"})
        api.reply("GET", BY_PATH, 200, RESOLVED)
        assert client(api).resolve_request(STACK_PATH).request_id == REQ
        assert len(api.calls) == 2

    @pytest.mark.parametrize("code", ["merge_commit_mismatch", "request_not_approved"])
    def test_other_conflicts_are_not_retried(self, api, code):
        api.reply("GET", BY_PATH, 409, {"code": code})
        with pytest.raises(ApiError, match=code):
            client(api).resolve_request(STACK_PATH)
        assert len(api.calls) == 1

    def test_errors_are_not_retried(self, api):
        api.reply("GET", BY_PATH, 404, {"code": "no_active_request"})
        with pytest.raises(ApiError, match="no_active_request") as exc:
            client(api).resolve_request(STACK_PATH)
        assert exc.value.status == 404 and len(api.calls) == 1


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

    def test_acquire_times_out_on_every_attempt(self, api):
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "granted", "current": LOCK})
        api.delay = 0.5
        with pytest.raises(ApiError) as exc:
            client(api, timeout=0.1, attempts=2).acquire_lock(STACK, REQ)
        assert exc.value.status is None  # no response: the grant may or may not have committed
        assert len(api.calls) == 2

    def test_get_lock(self, api):
        api.reply("GET", f"/v1/internal/stacks/{STACK}/lock", 200, LOCK)
        assert client(api).get_lock(STACK)["fencing_token"] == 48214

    def test_get_lock_unlocked(self, api):
        api.reply("GET", f"/v1/internal/stacks/{STACK}/lock", 404, {"code": "not_found"})
        assert client(api).get_lock(STACK) is None

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


class TestFreshCredentials:
    """OIDC tokens are single-use at the API (jti replay check), so each request needs its own."""

    def oidc_env(self, api):
        return {"ACTIONS_ID_TOKEN_REQUEST_URL": f"{api.url}/token", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "x"}

    def test_each_request_and_retry_gets_a_new_oidc_token(self, api):
        for n in (1, 2, 3):
            api.reply("GET", "/token", 200, {"value": f"jwt-{n}"})
        api.reply("POST", "/v1/runs", 503, {"code": "replay_store_unavailable"})
        api.reply("POST", "/v1/runs", 200, {"accepted": True})
        api.reply("GET", f"/v1/internal/stacks/{STACK}/lock", 200, LOCK)
        creds = FreshCredentials.start(TokenSource(self.oidc_env(api)), "aud")
        c = IacpApiClient(api.url, creds, sleep=lambda _: None)
        assert c.post_run({}).status == 200  # first attempt 503, retried
        c.get_lock(STACK)
        used = [call["headers"]["Authorization"] for call in api.calls if call["path"] != "/token"]
        assert used == ["Bearer jwt-1", "Bearer jwt-2", "Bearer jwt-3"]

    def test_internal_token_is_reused(self, api):
        api.reply("POST", "/v1/runs", 200, {"accepted": True})
        creds = FreshCredentials.start(TokenSource({"IACP_INTERNAL_TOKEN": INTERNAL}), "aud")
        c = IacpApiClient(api.url, creds, sleep=lambda _: None)
        c.post_run({})
        c.post_run({})
        assert {call["headers"]["Authorization"] for call in api.calls} == {f"Bearer {INTERNAL}"}

    def test_nothing_available(self):
        assert FreshCredentials.start(TokenSource({}), "aud") is None


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

    def test_error_log_becomes_the_error_message_of_a_failed_stage(self, tmp_path, make_stack, runner_env, capsys):
        root = make_stack("development", "platform", "smoke")
        log = tmp_path / "apply.log"
        log.write_text("aws_sqs_queue.q: Creating...\n│ Error: api error ThrottlingException: Rate exceeded\n")
        base = ["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "apply", "--stage", "apply",
                "--fencing-token", "9", "--terraform-exit-code", "1", "--error-log", str(log), "--dry-run"]
        main([*base, "--status", "failure"])
        payload = json.loads(capsys.readouterr().out)
        assert payload["error"]["message"].startswith("│ Error: api error ThrottlingException")
        # A passed stage, or an explicit message, ignores the log.
        main([*base, "--status", "success"])
        assert "error" not in json.loads(capsys.readouterr().out)
        main([*base, "--status", "failure", "--error-message", "explicit"])
        assert json.loads(capsys.readouterr().out)["error"]["message"] == "explicit"

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
        assert out.read_text() == "attempted=true\nclaimed=true\nfencing_token=48214\n"

    def test_failed_acquire_still_records_the_attempt(self, tmp_path, runner_env, api, monkeypatch):
        monkeypatch.setattr("tools.stack_pipeline.api_client.time.sleep", lambda _: None)
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 503, {"code": "unavailable"})
        out = tmp_path / "out"
        rc = main(["lock", "acquire", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url,
                   "--github-output", str(out)])
        assert rc == 1
        assert out.read_text() == "attempted=true\n"
        assert len(api.calls) == 3

    def test_release_without_token_releases_our_lock(self, runner_env, api, capsys):
        api.reply("GET", f"/v1/internal/stacks/{STACK}/lock", 200, LOCK)
        api.reply("DELETE", f"/v1/internal/stacks/{STACK}/lock", 200, {"released": LOCK, "promoted": None})
        assert main(["lock", "release", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url]) == 0
        assert api.calls[1]["query"]["fencing_token"] == ["48214"]
        assert "released (token 48214)" in capsys.readouterr().out

    def test_release_without_token_when_unlocked(self, runner_env, api, capsys):
        api.reply("GET", f"/v1/internal/stacks/{STACK}/lock", 404, {"code": "not_found"})
        assert main(["lock", "release", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url]) == 0
        assert [c["method"] for c in api.calls] == ["GET"]
        assert "nothing to release" in capsys.readouterr().out

    def test_release_without_token_leaves_another_requests_lock(self, runner_env, api, capsys):
        other = {**LOCK, "request_id": "33333333-3333-4333-8333-333333333333"}
        api.reply("GET", f"/v1/internal/stacks/{STACK}/lock", 200, other)
        assert main(["lock", "release", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url]) == 0
        assert [c["method"] for c in api.calls] == ["GET"]

    def test_acquire_resolves_the_ids_by_path(self, tmp_path, runner_env, api, capsys):
        api.reply("GET", BY_PATH, 200, RESOLVED)
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "granted", "current": LOCK})
        out = tmp_path / "out"
        rc = main(["lock", "acquire", "--stack-path", STACK_PATH, "--head-sha", SHA, "--api-url", api.url,
                   "--github-output", str(out)])
        assert rc == 0
        assert api.calls[0]["query"] == {"stack_path": [STACK_PATH], "head_sha": [SHA]}
        assert api.calls[1]["body"] == {"request_id": REQ}
        assert out.read_text() == (f"stack_id={STACK}\nrequest_id={REQ}\napproved_plan_hash={HASH}\n"
                                   "attempted=true\nclaimed=true\nfencing_token=48214\n")
        assert "resolved environments/development/payments/orders: request req-7k3p9x" in capsys.readouterr().out

    def test_acquire_with_both_ids_and_a_path_still_resolves(self, tmp_path, runner_env, api):
        """The lookup supplies the approved hash and proves the ids name the lock holder."""
        api.reply("GET", BY_PATH, 200, RESOLVED)
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "granted", "current": LOCK})
        out = tmp_path / "out"
        rc = main(["lock", "acquire", "--stack-path", STACK_PATH, "--stack-id", STACK, "--request-id", REQ,
                   "--api-url", api.url, "--github-output", str(out)])
        assert rc == 0
        assert [c["method"] for c in api.calls] == ["GET", "POST"]
        assert f"approved_plan_hash={HASH}\n" in out.read_text()

    def test_acquire_with_both_ids_that_disagree_fails_before_the_claim(self, runner_env, api, capsys):
        api.reply("GET", BY_PATH, 200, RESOLVED)
        other = "33333333-3333-4333-8333-333333333333"
        rc = main(["lock", "acquire", "--stack-path", STACK_PATH, "--stack-id", STACK, "--request-id", other,
                   "--api-url", api.url])
        assert rc == 1
        assert "not the request the API resolved" in capsys.readouterr().err
        assert [c["method"] for c in api.calls] == ["GET"]

    def test_acquire_with_ids_and_no_path_uses_them_as_they_are(self, runner_env, api):
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "granted", "current": LOCK})
        assert main(["lock", "acquire", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url]) == 0
        assert [c["method"] for c in api.calls] == ["POST"]

    def test_acquire_waits_for_the_merge_to_be_recorded(self, runner_env, api, monkeypatch):
        naps = []
        monkeypatch.setattr("tools.stack_pipeline.api_client.time.sleep", naps.append)
        api.reply("GET", BY_PATH, 409, {"code": "merge_not_recorded"})
        api.reply("GET", BY_PATH, 409, {"code": "merge_not_recorded"})
        api.reply("GET", BY_PATH, 200, RESOLVED)
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 200, {"lock": "granted", "current": LOCK})
        assert main(["lock", "acquire", "--stack-path", STACK_PATH, "--api-url", api.url]) == 0
        assert [c["method"] for c in api.calls] == ["GET", "GET", "GET", "POST"]
        assert naps == [5.0, 5.0]

    @pytest.mark.parametrize("status,body", [
        (404, {"code": "no_active_request"}),
        (409, {"code": "request_not_approved"}),
        (409, {"code": "merge_commit_mismatch"}),
        (403, {"code": "workflow_not_allowed"}),
    ])
    def test_a_lookup_that_finds_nothing_fails_closed(self, tmp_path, runner_env, api, capsys, status, body):
        api.reply("GET", BY_PATH, status, body)
        out = tmp_path / "out"
        rc = main(["lock", "acquire", "--stack-path", STACK_PATH, "--api-url", api.url, "--github-output", str(out)])
        assert rc == 1
        assert body["code"] in capsys.readouterr().err
        assert [c["method"] for c in api.calls] == ["GET"]  # no lock claim, no attempt recorded
        assert not out.exists() or "attempted" not in out.read_text()

    def test_acquire_refuses_ids_that_disagree_with_the_lookup(self, runner_env, api, capsys):
        api.reply("GET", BY_PATH, 200, RESOLVED)
        rc = main(["lock", "acquire", "--stack-path", STACK_PATH, "--request-id", "33333333-3333-4333-8333-333333333333",
                   "--api-url", api.url])
        assert rc == 1
        assert "not the request the API resolved" in capsys.readouterr().err
        assert [c["method"] for c in api.calls] == ["GET"]

    def test_acquire_needs_ids_or_a_path(self, runner_env, api, capsys):
        assert main(["lock", "acquire", "--api-url", api.url]) == 2
        assert "--stack-path" in capsys.readouterr().err
        assert api.calls == []

    def test_release_needs_ids(self, runner_env, api, capsys):
        assert main(["lock", "release", "--stack-path", STACK_PATH, "--api-url", api.url]) == 2
        assert "--stack-id and --request-id" in capsys.readouterr().err
        assert api.calls == []

    def test_unconfigured_skip(self, tmp_path, runner_env, capsys):
        out = tmp_path / "out"
        rc = main(["lock", "acquire", "--stack-id", STACK, "--request-id", REQ, "--skip-if-unconfigured",
                   "--github-output", str(out)])
        assert rc == 0
        assert out.read_text() == "attempted=false\nclaimed=false\n"
        assert "::notice" in capsys.readouterr().out

    def test_release_rejects_a_bad_token(self, runner_env, api, capsys):
        rc = main(["lock", "release", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url,
                   "--fencing-token", "0"])
        assert rc == 2
        assert "--fencing-token" in capsys.readouterr().err
        assert api.calls == []

    def test_queued_fails(self, runner_env, api, capsys):
        api.reply("POST", f"/v1/internal/stacks/{STACK}/lock", 202, {"lock": "queued", "queued_behind": REQ, "current": LOCK})
        assert main(["lock", "acquire", "--stack-id", STACK, "--request-id", REQ, "--api-url", api.url]) == 1
        assert "not granted" in capsys.readouterr().err


def test_annotation_cannot_start_a_new_workflow_command():
    assert annotation("bad\n::add-mask::x 100%\r") == "bad%0A::add-mask::x 100%25%0D"


def test_server_text_in_warnings_stays_on_one_line(tmp_path, make_stack, runner_env, api, capsys):
    root = make_stack("development", "platform", "smoke")
    api.reply("POST", "/v1/runs", 400, {"code": "bad_request", "title": "x\n::error::injected"})
    rc = main(["--repo-root", str(tmp_path), "report", str(root), "--pipeline", "validate", "--stage", "summary",
               "--status", "passed", "--api-url", api.url, "--warn-on-error"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "\n::error::" not in out and "%0A::error::injected" in out


class TestStateSurgeryReport:
    def test_posts_and_retries(self, api):
        path = "/v1/internal/state-surgery"
        api.reply("POST", path, 503, {"code": "unavailable"})
        api.reply("POST", path, 201, {"stack_id": STACK, "duplicate": False})
        resp = client(api).post_state_surgery({"outcome": "succeeded"})
        assert resp.status == 201 and resp.body["stack_id"] == STACK
        assert len(api.calls) == 2 and api.calls[1]["body"] == {"outcome": "succeeded"}

    def test_rejection_raises(self, api):
        api.reply("POST", "/v1/internal/state-surgery", 403, {"code": "workflow_not_allowed"})
        with pytest.raises(ApiError) as exc:
            client(api).post_state_surgery({})
        assert exc.value.status == 403 and exc.value.code == "workflow_not_allowed"
