"""The reaper: list what is due, warn, tear down. HTTP lives in ``TtlApiClient``."""

from __future__ import annotations

import uuid
from typing import Any, Mapping, Protocol

from tools.stack_pipeline.api_client import ApiError, IacpApiClient

from .models import (
    Action,
    DEFAULT_MAX_TEARDOWNS,
    DueList,
    Outcome,
    RunResult,
    classify_failure,
    parse_due,
    plan_actions,
)

DUE_PATH = "/v1/internal/ttl/due"
WARN_PATH = "/v1/internal/ttl/{stack_id}/warn"
TEARDOWN_PATH = "/v1/internal/ttl/{stack_id}/teardown"


class TtlApi(Protocol):
    """What the reaper needs from the platform; ``TtlApiClient`` is the HTTP adapter."""

    def due(self) -> DueList: ...

    def warn(self, stack_id: str) -> Mapping[str, Any]: ...

    def teardown(self, stack_id: str) -> tuple[int, Mapping[str, Any]]: ...


class TtlApiClient(IacpApiClient):
    """The /v1/internal/ttl/* calls, on the shared transport (OIDC or internal token, no
    redirects, retries on 429/5xx). Warn and teardown are idempotent on the server, so a
    retried call cannot warn twice or open two destroy requests."""

    def due(self) -> DueList:
        return parse_due(self._call("GET", DUE_PATH, None, ok=(200,)).body)

    def warn(self, stack_id: str) -> Mapping[str, Any]:
        path = WARN_PATH.format(stack_id=str(uuid.UUID(stack_id)))
        return self._call("POST", path, None, ok=(200,)).body

    def teardown(self, stack_id: str) -> tuple[int, Mapping[str, Any]]:
        path = TEARDOWN_PATH.format(stack_id=str(uuid.UUID(stack_id)))
        resp = self._call("POST", path, None, ok=(200, 202))
        return resp.status, resp.body


class Reaper:
    """One hourly pass. A failure on one stack is recorded and the pass continues, so one
    broken stack cannot keep the others from being cleaned up; the caller exits non-zero
    when any action failed."""

    def __init__(self, api: TtlApi, *, max_teardowns: int = DEFAULT_MAX_TEARDOWNS, dry_run: bool = False) -> None:
        self._api = api
        self._max_teardowns = max_teardowns
        self._dry_run = dry_run

    def run(self) -> RunResult:
        actions, deferred = plan_actions(self._api.due(), self._max_teardowns)
        result = RunResult(deferred=deferred)
        for action in actions:
            result.outcomes.append(self._dry(action) if self._dry_run else self._act(action))
        return result

    @staticmethod
    def _dry(action: Action) -> Outcome:
        return Outcome(action, "dry-run", f"would {action.kind} (expires {action.stack.expires_at})")

    def _act(self, action: Action) -> Outcome:
        try:
            if action.kind == "teardown":
                return self._teardown(action)
            return self._warn(action)
        except ApiError as exc:
            status = classify_failure(exc.status, exc.code)
            return Outcome(action, status, str(exc))

    def _teardown(self, action: Action) -> Outcome:
        status, body = self._api.teardown(action.stack.stack_id)
        if status == 202 and body.get("status") == "created":
            queued = " (queued behind another request)" if body.get("queued") else ""
            return Outcome(action, "done", f"destroy request {body.get('short_id', '?')} opened{queued}")
        return Outcome(action, "already", "a destroy request is already open")

    def _warn(self, action: Action) -> Outcome:
        body = self._api.warn(action.stack.stack_id)
        if body.get("warned") is True:
            return Outcome(action, "done", f"warned (expires {action.stack.expires_at})")
        return Outcome(action, "already", str(body.get("reason") or "not in the warning window"))
