"""Pure logic, the reaper against a fake API, and the CLI against a real loopback HTTP server."""

from __future__ import annotations

import json
import threading
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Mapping
from urllib.parse import urlsplit

import pytest

from tools.stack_pipeline.api_client import ApiError
from tools.ttl_reaper.cli import main
from tools.ttl_reaper.models import (
    Action,
    DueList,
    DueListError,
    DueStack,
    Outcome,
    RunResult,
    classify_failure,
    needs_attention,
    parse_due,
    plan_actions,
    render_summary,
)
from tools.ttl_reaper.reaper import Reaper

INTERNAL = "internal-token-0123456789abcdef0123456789"


def sid(n: int) -> str:
    return f"00000000-0000-4000-8000-{n:012d}"


def item(n: int, env: str = "development", **extra: Any) -> dict[str, Any]:
    return {"stack_id": sid(n), "path": f"environments/{env}/payments/s{n}", "team": "payments", "environment": env,
            "expires_at": "2026-11-26T11:00:00Z", **extra}


def stack(n: int) -> DueStack:
    return DueStack(sid(n), f"environments/development/payments/s{n}", "payments", "development", "2026-11-26T11:00:00Z")


# ---- pure ----------------------------------------------------------------------------


def test_parse_due_validates_and_dedupes():
    due = parse_due({"warnings": [item(1), item(1)], "teardowns": [item(2), item(3)]})
    assert [s.stack_id for s in due.warnings] == [sid(1)]
    assert [s.stack_id for s in due.teardowns] == [sid(2), sid(3)]


def test_an_expired_stack_is_torn_down_not_warned():
    due = parse_due({"warnings": [item(1), item(2)], "teardowns": [item(2)]})
    assert [s.stack_id for s in due.warnings] == [sid(1)]


@pytest.mark.parametrize(
    "body",
    [
        {},
        {"warnings": [], "teardowns": "x"},
        {"warnings": [], "teardowns": [{"stack_id": "nope", "path": "p", "team": "t", "environment": "development", "expires_at": "x"}]},
        {"warnings": [], "teardowns": [item(1, path="")]},
        {"warnings": [], "teardowns": ["x"]},
        {"warnings": [], "teardowns": [item(1, env="production")]},
        {"warnings": [item(1, env="production")], "teardowns": []},
    ],
)
def test_a_malformed_or_production_list_is_refused_whole(body):
    with pytest.raises(DueListError):
        parse_due(body)


def test_plan_puts_teardowns_first_and_leaves_blocked_stacks_out():
    due = DueList(warnings=(stack(9),), teardowns=(stack(1), stack(2)), blocked=(stack(7),))
    assert [(a.kind, a.stack.stack_id) for a in plan_actions(due)] == [("teardown", sid(1)), ("teardown", sid(2)), ("warn", sid(9))]


def test_a_blocked_stack_is_never_listed_as_a_teardown():
    body = {"warnings": [], "teardowns": [item(1), item(2)], "blocked": [item(2)]}
    due = parse_due(body)
    assert [s.stack_id for s in due.teardowns] == [sid(1)] and [s.stack_id for s in due.blocked] == [sid(2)]
    assert parse_due({"warnings": [], "teardowns": []}).blocked == ()  # an API without the list


@pytest.mark.parametrize("code,want", [("ttl_not_due", "skipped"), ("ttl_backoff", "skipped"), ("ttl_blocked", "blocked"), ("stack_busy", "skipped"), ("stack_not_found", "skipped"),
                                        ("production_not_ephemeral", "failed"), ("", "failed"), ("internal", "failed")])
def test_classify_failure(code, want):
    assert classify_failure(409, code) == want


NOW = datetime(2026, 11, 27, 12, 0, tzinfo=timezone.utc)  # 25 h after the fixtures' expiry


def test_needs_attention_flags_long_busy_skips_and_blocked_stacks():
    a = Action("teardown", stack(1))  # expired 2026-11-26T11:00:00Z
    assert needs_attention(Outcome(a, "skipped", "busy", "stack_busy"), NOW)
    assert not needs_attention(Outcome(a, "skipped", "busy", "stack_busy"), NOW - timedelta(hours=2)), "expired only 23 h ago"
    assert needs_attention(Outcome(a, "blocked", "no attempts left", "ttl_blocked"), NOW)
    assert not needs_attention(Outcome(a, "skipped", "extended", "ttl_not_due"), NOW)
    assert not needs_attention(Outcome(a, "skipped", "backing off", "ttl_backoff"), NOW)
    assert not needs_attention(Outcome(a, "done"), NOW)


def test_summary_escapes_table_cells():
    r = RunResult(outcomes=[Outcome(Action("teardown", stack(1)), "done", "a|b\nc")], deferred=[stack(2)])
    md = render_summary(r)
    assert "a\\|b c" in md and "1 more expired stack(s)" in md
    assert "Nothing is due." in render_summary(RunResult())


# ---- reaper with a fake API ----------------------------------------------------------


class FakeApi:
    def __init__(self, due: DueList, teardown: Mapping[str, Any] | None = None, warn: Mapping[str, Any] | None = None) -> None:
        self._due, self.torn, self.warned = due, [], []
        self._teardown = teardown or {}
        self._warn = warn or {}

    def due(self) -> DueList:
        return self._due

    def warn(self, stack_id: str):
        self.warned.append(stack_id)
        out = self._warn.get(stack_id, {"warned": True})
        if isinstance(out, Exception):
            raise out
        return out

    def teardown(self, stack_id: str):
        self.torn.append(stack_id)
        out = self._teardown.get(stack_id, (202, {"status": "created", "short_id": "req-abc"}))
        if isinstance(out, Exception):
            raise out
        return out


def test_reaper_acts_and_reports_each_outcome():
    api = FakeApi(
        DueList(warnings=(stack(5), stack(6)), teardowns=(stack(1), stack(2), stack(3), stack(4))),
        teardown={sid(2): (200, {"status": "already_pending"}), sid(3): ApiError("busy", 409, "stack_busy"),
                  sid(4): ApiError("boom", 500, "internal")},
        warn={sid(6): {"warned": False, "reason": "not_in_warning_window: none"}},
    )
    result = Reaper(api).run()
    got = {(o.action.kind, o.action.stack.stack_id): o.status for o in result.outcomes}
    assert got == {("teardown", sid(1)): "done", ("teardown", sid(2)): "already", ("teardown", sid(3)): "skipped",
                   ("teardown", sid(4)): "failed", ("warn", sid(5)): "done", ("warn", sid(6)): "already"}
    assert not result.ok and [o.action.stack.stack_id for o in result.failures] == [sid(4)]
    # One failure does not stop the pass: every stack was attempted.
    assert api.torn == [sid(1), sid(2), sid(3), sid(4)] and api.warned == [sid(5), sid(6)]


def test_dry_run_changes_nothing():
    api = FakeApi(DueList(warnings=(stack(5),), teardowns=(stack(1),)))
    result = Reaper(api, dry_run=True).run()
    assert api.torn == [] and api.warned == [] and result.ok
    assert [o.status for o in result.outcomes] == ["dry-run", "dry-run"]


def test_the_teardown_cap_counts_only_opened_destroys():
    api = FakeApi(DueList(teardowns=tuple(stack(n) for n in range(1, 6))))
    result = Reaper(api, max_teardowns=2).run()
    assert api.torn == [sid(1), sid(2)] and [s.stack_id for s in result.deferred] == [sid(3), sid(4), sid(5)]


def test_busy_backoff_and_blocked_stacks_do_not_crowd_out_fresh_teardowns():
    cap = 3
    busy = [stack(n) for n in range(1, 31)]            # many more than the cap, oldest first
    backoff = [stack(n) for n in range(31, 61)]
    fresh = [stack(n) for n in range(61, 66)]          # five expired stacks that can be torn down
    blocked = [stack(n) for n in range(100, 140)]
    teardown = {s.stack_id: ApiError("busy", 409, "stack_busy") for s in busy}
    teardown.update({s.stack_id: ApiError("later", 409, "ttl_backoff") for s in backoff})
    api = FakeApi(DueList(teardowns=tuple(busy + backoff + fresh), blocked=tuple(blocked)), teardown=teardown)
    result = Reaper(api, max_teardowns=cap).run()
    fresh_ids = {f.stack_id for f in fresh}
    assert [i for i in api.torn if i in fresh_ids] == [s.stack_id for s in fresh[:cap]], "the fresh stacks were torn down, up to the cap"
    assert [s.stack_id for s in result.deferred] == [s.stack_id for s in fresh[cap:]]
    assert len(api.torn) == len(busy) + len(backoff) + cap, "skips cost a call, never a slot"
    assert [o.status for o in result.outcomes if o.status == "blocked"] == ["blocked"] * len(blocked)
    assert not set(api.torn) & {s.stack_id for s in blocked}, "a blocked stack is reported, not called"
    assert result.ok


# ---- CLI against a loopback server ---------------------------------------------------


class Server:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str, str]] = []
        self.routes: dict[tuple[str, str], tuple[int, dict[str, Any]]] = {}
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def _serve(self) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                if length:
                    self.rfile.read(length)
                path = urlsplit(self.path).path
                outer.calls.append((self.command, path, self.headers.get("Authorization", "")))
                status, body = outer.routes.get((self.command, path), (404, {"code": "not_found"}))
                raw = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            do_GET = do_POST = _serve

            def log_message(self, *args: Any) -> None:
                pass

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def __enter__(self) -> "Server":
        self.thread.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()


def test_cli_skips_with_a_notice_when_the_api_is_not_configured(capsys):
    assert main(["--skip-if-unconfigured", "--api-url", ""], env={}) == 0
    assert "::notice" in capsys.readouterr().out
    assert main(["--api-url", ""], env={}) == 1  # without the flag a missing URL is an error


def test_cli_end_to_end(tmp_path: Path, capsys):
    summary = tmp_path / "summary.md"
    with Server() as api:
        api.routes[("GET", "/v1/internal/ttl/due")] = (200, {"warnings": [item(5)], "teardowns": [item(1), item(2)]})
        api.routes[("POST", f"/v1/internal/ttl/{sid(1)}/teardown")] = (202, {"status": "created", "short_id": "req-1"})
        api.routes[("POST", f"/v1/internal/ttl/{sid(2)}/teardown")] = (409, {"code": "ttl_not_due", "title": "not expired"})
        api.routes[("POST", f"/v1/internal/ttl/{sid(5)}/warn")] = (200, {"warned": True})
        code = main(["--api-url", api.url, "--summary-file", str(summary)], env={"IACP_INTERNAL_TOKEN": INTERNAL})
    out = capsys.readouterr().out
    assert code == 0, out
    assert "teardown environments/development/payments/s1: done" in out
    assert "teardown environments/development/payments/s2: skipped" in out
    assert "warn environments/development/payments/s5: done" in out
    assert all(auth == f"Bearer {INTERNAL}" for _, _, auth in api.calls)
    assert [(m, p) for m, p, _ in api.calls][0] == ("GET", "/v1/internal/ttl/due")
    assert "| teardown |" in summary.read_text()


def test_cli_fails_when_an_action_fails(capsys):
    with Server() as api:
        api.routes[("GET", "/v1/internal/ttl/due")] = (200, {"warnings": [], "teardowns": [item(1)]})
        api.routes[("POST", f"/v1/internal/ttl/{sid(1)}/teardown")] = (403, {"code": "workflow_not_allowed"})
        code = main(["--api-url", api.url], env={"IACP_INTERNAL_TOKEN": INTERNAL})
    assert code == 1 and "::error title=TTL teardown failed" in capsys.readouterr().out


def test_cli_dry_run_makes_no_mutating_call(capsys):
    with Server() as api:
        api.routes[("GET", "/v1/internal/ttl/due")] = (200, {"warnings": [item(5)], "teardowns": [item(1)]})
        code = main(["--api-url", api.url, "--dry-run"], env={"IACP_INTERNAL_TOKEN": INTERNAL})
    assert code == 0 and [m for m, _, _ in api.calls] == ["GET"]
    assert "would teardown" in capsys.readouterr().out


def test_cli_refuses_a_malformed_list_and_a_missing_credential(capsys):
    with Server() as api:
        api.routes[("GET", "/v1/internal/ttl/due")] = (200, {"warnings": [], "teardowns": [item(1, env="production")]})
        assert main(["--api-url", api.url], env={"IACP_INTERNAL_TOKEN": INTERNAL}) == 1
        assert [m for m, _, _ in api.calls] == ["GET"]
    assert main(["--api-url", "http://127.0.0.1:9"], env={}) == 1
    out = capsys.readouterr().out
    assert "production stacks are never ephemeral" in out and "no credential" in out


def test_plain_http_to_a_remote_host_is_refused(capsys):
    assert main(["--api-url", "http://api.example.com"], env={"IACP_INTERNAL_TOKEN": INTERNAL}) == 1
    assert "https" in capsys.readouterr().out


def test_cli_warns_about_busy_and_blocked_stacks_without_failing(capsys):
    with Server() as api:
        api.routes[("GET", "/v1/internal/ttl/due")] = (200, {"warnings": [], "teardowns": [item(1, expires_at="2026-01-01T00:00:00Z"), item(3)], "blocked": [item(2)]})
        api.routes[("POST", f"/v1/internal/ttl/{sid(1)}/teardown")] = (409, {"code": "stack_busy", "title": "an apply is in flight"})
        api.routes[("POST", f"/v1/internal/ttl/{sid(3)}/teardown")] = (409, {"code": "ttl_backoff", "title": "later"})
        code = main(["--api-url", api.url], env={"IACP_INTERNAL_TOKEN": INTERNAL})
    out = capsys.readouterr().out
    assert code == 0, out
    assert out.count("::warning title=TTL") == 2
    assert "::warning title=TTL skipped::teardown environments/development/payments/s1" in out
    assert "::warning title=TTL blocked::teardown environments/development/payments/s2" in out
    assert ("POST", f"/v1/internal/ttl/{sid(2)}/teardown") not in [(m, p) for m, p, _ in api.calls], "blocked stacks are not called"
    assert "s3: skipped" in out and "::warning title=TTL skipped::teardown environments/development/payments/s3" not in out
