"""HTTP adapters for the Platform API: bearer tokens, run reports and the stack lock.

``TokenSource`` picks the caller's credential: a GitHub OIDC token for the
configured audience when the job has ``id-token: write``, else the shared
``IACP_INTERNAL_TOKEN`` (docs/03 section 6.9). ``IacpApiClient`` calls
``POST /v1/runs`` and ``/v1/internal/stacks/{id}/lock``. Neither follows a
redirect, because urllib would replay the bearer token to the new location.

A GitHub OIDC token is single-use: the API records its ``jti`` and answers a second
use with 401 (docs/03 section 6.9). ``FreshCredentials`` therefore mints a new OIDC
token for every request, retries included; the shared internal token is reused.
"""

from __future__ import annotations

import json
import re
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass
from typing import Any, Callable, Mapping, TextIO

from .report import api_base_url, describe_problem

RUNS_PATH = "/v1/runs"
LOCK_PATH = "/v1/internal/stacks/{stack_id}/lock"
REQUEST_BY_PATH = "/v1/internal/requests/by-path"
STATE_SURGERY_PATH = "/v1/internal/state-surgery"
USER_AGENT = "iacp-stack-pipeline"


class ApiError(RuntimeError):
    """The API rejected a call or could not be reached."""

    def __init__(self, message: str, status: int | None = None, code: str = "") -> None:
        super().__init__(message)
        self.status = status
        self.code = code


class _RefuseRedirects(urllib.request.HTTPRedirectHandler):
    """Never follow a redirect; the 3xx surfaces as an HTTPError."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001 (urllib API)
        return None


def build_opener() -> urllib.request.OpenerDirector:
    """An opener that refuses redirects."""
    return urllib.request.build_opener(_RefuseRedirects())


def _json(raw: str) -> dict[str, Any]:
    try:
        value = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        return {}
    return value if isinstance(value, dict) else {}


@dataclass(frozen=True)
class Credential:
    """A bearer token and where it came from (``oidc`` or ``internal``). Never printed."""

    token: str
    kind: str

    def __repr__(self) -> str:  # keep the token out of tracebacks and logs
        return f"Credential(kind={self.kind!r}, token=<redacted>)"


class TokenSource:
    """Chooses the bearer token for one invocation.

    OIDC first: when the runner exposes ``ACTIONS_ID_TOKEN_REQUEST_URL`` /
    ``ACTIONS_ID_TOKEN_REQUEST_TOKEN`` (the job has ``id-token: write``), a
    token is requested for ``audience``. A job that can mint OIDC tokens never
    falls back to the shared token: an empty audience or a failed OIDC request
    raises, so a half-configured repository cannot silently run unscoped (the
    backend does not scope the shared token to a workflow). Only a job without
    ``id-token: write`` uses the shared internal token. Every token is registered with
    ``::add-mask::`` inside GitHub Actions so the runner masks it in all logs.
    """

    def __init__(
        self,
        env: Mapping[str, str],
        *,
        opener: urllib.request.OpenerDirector | None = None,
        timeout: float = 10.0,
        stdout: TextIO | None = None,
    ) -> None:
        self._env = env
        self._opener = opener or build_opener()
        self.timeout = timeout
        self._stdout = stdout

    @property
    def oidc_available(self) -> bool:
        return bool(self._env.get("ACTIONS_ID_TOKEN_REQUEST_URL") and self._env.get("ACTIONS_ID_TOKEN_REQUEST_TOKEN"))

    def credential(self, audience: str) -> Credential | None:
        """The token to use, or None when neither OIDC nor the internal token is available."""
        cred: Credential | None = None
        if self.oidc_available:
            if not audience.strip():
                raise ApiError("this job can request GitHub OIDC tokens but no audience is configured "
                               "(set IACP_OIDC_AUDIENCE); refusing to fall back to the shared token")
            cred = Credential(self._oidc_token(audience.strip()), "oidc")
        elif self._env.get("IACP_INTERNAL_TOKEN", "").strip():
            cred = Credential(self._env["IACP_INTERNAL_TOKEN"].strip(), "internal")
        if cred is not None:
            self._mask(cred.token)
        return cred

    def _oidc_token(self, audience: str) -> str:
        base = self._env["ACTIONS_ID_TOKEN_REQUEST_URL"]
        sep = "&" if urllib.parse.urlsplit(base).query else "?"
        url = f"{base}{sep}{urllib.parse.urlencode({'audience': audience})}"
        req = urllib.request.Request(
            url,
            headers={"Authorization": f"bearer {self._env['ACTIONS_ID_TOKEN_REQUEST_TOKEN']}", "Accept": "application/json",
                     "User-Agent": USER_AGENT},
        )
        try:
            with self._opener.open(req, timeout=self.timeout) as resp:
                value = _json(resp.read().decode("utf-8")).get("value")
        except urllib.error.HTTPError as exc:
            raise ApiError(f"GitHub OIDC token request failed: HTTP {exc.code}", exc.code) from None
        except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
            raise ApiError(f"GitHub OIDC token request failed: {getattr(exc, 'reason', exc)}") from None
        if not isinstance(value, str) or not value:
            raise ApiError("GitHub OIDC token response carried no token")
        return value

    def _mask(self, token: str) -> None:
        if self._env.get("GITHUB_ACTIONS") == "true" and self._stdout is not None:
            self._stdout.write(f"::add-mask::{token}\n")
            self._stdout.flush()


class FreshCredentials:
    """A credential per request. The first one is fetched up front (so a missing
    credential fails before any call); later OIDC requests mint a new token each time."""

    def __init__(self, source: TokenSource, audience: str, first: Credential) -> None:
        self._source = source
        self._audience = audience
        self._pending: Credential | None = first
        self._static = first if first.kind != "oidc" else None

    @classmethod
    def start(cls, source: TokenSource, audience: str) -> FreshCredentials | None:
        first = source.credential(audience)
        return None if first is None else cls(source, audience, first)

    def __call__(self) -> Credential:
        if self._pending is not None:
            cred, self._pending = self._pending, None
            return cred
        if self._static is not None:
            return self._static
        cred = self._source.credential(self._audience)
        if cred is None:  # the runner stopped exposing OIDC mid-run
            raise ApiError("no credential for the next API call")
        return cred


@dataclass(frozen=True)
class ApiResponse:
    status: int
    body: dict[str, Any]


@dataclass(frozen=True)
class LockClaim:
    """A held stack lock: the fencing token every later report and the release carry."""

    outcome: str
    fencing_token: int
    request_id: str
    expires_at: str


STACK_PATH_RE = re.compile(r"^environments/(development|staging|production)/[a-z][a-z0-9-]{1,38}/[a-z][a-z0-9-]{1,40}$")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
HASH_RE = re.compile(r"^[0-9a-f]{64}$")


@dataclass(frozen=True)
class ResolvedRequest:
    """The request that holds a stack's lock and is ready to apply (GET /v1/internal/requests/by-path)."""

    stack_id: str
    request_id: str
    request_short_id: str
    fencing_token: int
    approved_plan_hash: str  # empty when the API recorded none


class IacpApiClient:
    """Calls the Platform API's machine endpoints. ``credential`` is a fixed Credential
    or a callable returning one per request (FreshCredentials for single-use OIDC tokens)."""

    RETRYABLE = frozenset({429, 500, 502, 503, 504})

    def __init__(
        self,
        base_url: str,
        credential: Credential | Callable[[], Credential],
        *,
        timeout: float = 20.0,
        attempts: int = 3,
        backoff: float = 2.0,
        opener: urllib.request.OpenerDirector | None = None,
        sleep: Callable[[float], None] | None = None,
    ) -> None:
        self.base_url = api_base_url(base_url)
        self._credential = credential
        self.timeout = timeout
        self.attempts = max(1, attempts)
        self.backoff = backoff
        self._opener = opener or build_opener()
        self._sleep = sleep or (lambda seconds: time.sleep(seconds))

    # ---- endpoints ---------------------------------------------------------------

    def post_run(self, payload: Mapping[str, Any]) -> ApiResponse:
        """POST /v1/runs. Retried on 429/5xx: a stage report replaces its earlier entry."""
        return self._call("POST", RUNS_PATH, payload, ok=(200,))

    def post_state_surgery(self, payload: Mapping[str, Any]) -> ApiResponse:
        """POST /v1/internal/state-surgery: the break-glass audit report. Retried on 429/5xx:
        the API records one event per run attempt (a resend answers 200 duplicate)."""
        return self._call("POST", STATE_SURGERY_PATH, payload, ok=(200, 201))

    def resolve_request(self, stack_path: str, head_sha: str = "", *, merge_wait_attempts: int = 6,
                        merge_wait_seconds: float = 5.0) -> ResolvedRequest:
        """GET /v1/internal/requests/by-path: the stack id, the request holding the stack lock
        and its approved plan hash. Fails closed: a 404 (no stack, no lock) or 409 (holder not
        approved, merge commit mismatch) is an ApiError, and so is an answer that does not
        carry valid ids. ``merge_not_recorded`` (the pr_merged webhook has not recorded the
        merge yet) is the one retryable answer: it is asked again up to ``merge_wait_attempts``
        times, ``merge_wait_seconds`` apart, then raised."""
        for attempt in range(1, max(1, merge_wait_attempts) + 1):
            try:
                return self._resolve_request(stack_path, head_sha)
            except ApiError as exc:
                if exc.code != "merge_not_recorded" or attempt >= max(1, merge_wait_attempts):
                    raise
                self._sleep(merge_wait_seconds)
        raise AssertionError("unreachable")

    def _resolve_request(self, stack_path: str, head_sha: str) -> ResolvedRequest:
        if not STACK_PATH_RE.match(stack_path):
            raise ValueError(f"stack path must look like environments/<env>/<team>/<stack>, got {stack_path!r}")
        query = {"stack_path": stack_path}
        if head_sha:
            if not SHA_RE.match(head_sha):
                raise ValueError(f"head sha must be 40 lowercase hex characters, got {head_sha!r}")
            query["head_sha"] = head_sha
        body = self._call("GET", REQUEST_BY_PATH + "?" + urllib.parse.urlencode(query), None, ok=(200,)).body
        token = body.get("fencing_token")
        approved = body.get("approved_plan_hash") or ""
        try:
            stack_id, request_id = _uuid(str(body.get("stack_id", "")), "stack id"), _uuid(str(body.get("request_id", "")), "request id")
        except ValueError as exc:
            raise ApiError(f"GET {REQUEST_BY_PATH}: response carried no valid id: {exc}") from None
        if not isinstance(token, int) or isinstance(token, bool) or token <= 0:
            raise ApiError(f"GET {REQUEST_BY_PATH}: response carried no fencing token")
        if approved and not HASH_RE.match(str(approved)):
            raise ApiError(f"GET {REQUEST_BY_PATH}: response carried a malformed plan hash")
        return ResolvedRequest(stack_id=stack_id, request_id=request_id, request_short_id=str(body.get("request_short_id", "")),
                               fencing_token=token, approved_plan_hash=str(approved))

    def acquire_lock(self, stack_id: str, request_id: str) -> LockClaim:
        """POST /v1/internal/stacks/{id}/lock. ``granted`` or ``held`` is the caller's lock;
        ``queued`` (202) means another request holds it, which raises."""
        path = LOCK_PATH.format(stack_id=_uuid(stack_id, "stack id"))
        rid = _uuid(request_id, "request id")
        resp = self._call("POST", path, {"request_id": rid}, ok=(200, 202))
        outcome = str(resp.body.get("lock", ""))
        current = resp.body.get("current") or {}
        if resp.status == 202 or outcome not in ("granted", "held"):
            behind = resp.body.get("queued_behind") or current.get("request_id", "?")
            raise ApiError(f"stack lock not granted: {outcome or 'unknown'}, held by request {behind}", resp.status, "stack_locked")
        if str(current.get("request_id", "")).lower() != rid:
            raise ApiError("stack lock response names another request", resp.status, "stack_locked")
        token = current.get("fencing_token")
        if not isinstance(token, int) or isinstance(token, bool) or token <= 0:
            raise ApiError("stack lock response carried no fencing token", resp.status)
        return LockClaim(outcome=outcome, fencing_token=token, request_id=rid,
                         expires_at=str(current.get("expires_at", "")))

    def get_lock(self, stack_id: str) -> dict[str, Any] | None:
        """GET /v1/internal/stacks/{id}/lock: the current lock, or None when unlocked (404)."""
        try:
            return self._call("GET", LOCK_PATH.format(stack_id=_uuid(stack_id, "stack id")), None, ok=(200,)).body
        except ApiError as exc:
            if exc.status == 404:
                return None
            raise

    def release_lock(self, stack_id: str, request_id: str, fencing_token: int) -> ApiResponse:
        """DELETE /v1/internal/stacks/{id}/lock?request_id=&fencing_token=. Not retried:
        a replay after a lost response would only meet 409 stale_fencing_token."""
        query = urllib.parse.urlencode({"request_id": _uuid(request_id, "request id"), "fencing_token": int(fencing_token)})
        path = LOCK_PATH.format(stack_id=_uuid(stack_id, "stack id")) + "?" + query
        return self._call("DELETE", path, None, ok=(200,), attempts=1)

    # ---- transport ---------------------------------------------------------------

    def _request(self, method: str, path: str, payload: Mapping[str, Any] | None) -> urllib.request.Request:
        cred = self._credential() if callable(self._credential) else self._credential
        headers = {"Authorization": f"Bearer {cred.token}", "Accept": "application/json", "User-Agent": USER_AGENT}
        data = None
        if payload is not None:
            data = json.dumps(payload, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        return urllib.request.Request(self.base_url + path, data=data, method=method, headers=headers)

    def _call(
        self, method: str, path: str, payload: Mapping[str, Any] | None, *, ok: tuple[int, ...], attempts: int | None = None
    ) -> ApiResponse:
        tries = attempts or self.attempts
        label = f"{method} {path.split('?', 1)[0]}"
        last: ApiError | None = None
        for attempt in range(1, tries + 1):
            try:
                with self._opener.open(self._request(method, path, payload), timeout=self.timeout) as resp:
                    raw = resp.read().decode("utf-8")
                    if resp.status not in ok:
                        raise ApiError(f"{label}: unexpected HTTP {resp.status}", resp.status)
                    return ApiResponse(resp.status, _json(raw))
            except urllib.error.HTTPError as exc:
                raw = exc.read().decode("utf-8", errors="replace")
                problem = _json(raw)
                if 300 <= exc.code < 400:
                    location = exc.headers.get("Location", "") if exc.headers else ""
                    raise ApiError(f"{label}: HTTP {exc.code} redirect to {location!r} refused; "
                                   "set IACP_API_URL to the final URL", exc.code) from None
                last = ApiError(f"{label}: HTTP {exc.code}: {describe_problem(problem, raw)}", exc.code,
                                str(problem.get("code", "")))
                if exc.code not in self.RETRYABLE:
                    raise last from None
            except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
                last = ApiError(f"{label}: {getattr(exc, 'reason', exc)}")
            if attempt < tries:
                self._sleep(self.backoff * attempt)
        assert last is not None
        raise last


def _uuid(value: str, what: str) -> str:
    try:
        return str(uuid.UUID(value.strip()))
    except (ValueError, AttributeError):
        raise ValueError(f"{what} must be a UUID, got {value!r}") from None
