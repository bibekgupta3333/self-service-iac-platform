"""Catalog indexing: ``POST /v1/internal/catalog/index`` (WBS 3.7).

``build_index_payload`` is pure and mirrors ``catalog.IndexCommand`` in
backend/internal/catalog/index.go. The backend decodes strictly (unknown
fields are rejected, also inside ``outputs``), so only fields it declares
are sent. ``CatalogClient`` is the HTTP adapter.
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any, Callable, Mapping
from urllib.parse import urlsplit

from .models import SemVer
from .versioning import git_tag

INDEX_PATH = "/v1/internal/catalog/index"
AWS_PROVIDER = "hashicorp/aws"
_LOOPBACK_HOSTS = frozenset({"localhost", "127.0.0.1", "::1"})


class CatalogError(RuntimeError):
    """The catalog rejected the release or could not be reached."""

    def __init__(self, message: str, status: int | None = None, body: str = "") -> None:
        super().__init__(message)
        self.status = status
        self.body = body


def build_index_payload(
    manifest: Mapping[str, Any],
    commit_sha: str,
    released_by: str,
    changelog: str | None = None,
) -> dict[str, Any]:
    """Build the IndexCommand JSON body from a parsed module.yaml. Pure."""
    metadata = manifest.get("metadata") or {}
    compatibility = manifest.get("compatibility") or {}
    name = str(metadata.get("name", ""))
    version = SemVer.parse(str(metadata.get("version", "")))
    payload: dict[str, Any] = {
        "module": name,
        "version": str(version),
        "git_tag": git_tag(name, version),
        "commit_sha": commit_sha,
        "display_name": metadata.get("displayName", ""),
        "description": metadata.get("description", ""),
        "category": metadata.get("category", ""),
        "owners": list(metadata.get("owners") or []),
        "inputs_schema": manifest.get("inputs"),
        "outputs": [
            {"name": o["name"], "description": o.get("description", ""), "sensitive": bool(o.get("sensitive", False))}
            for o in manifest.get("outputs") or []
            if isinstance(o, Mapping) and "name" in o
        ],
        "tf_version_range": compatibility.get("terraform", ""),
        "aws_provider_range": (compatibility.get("providers") or {}).get(AWS_PROVIDER, ""),
        "released_by": released_by,
    }
    if manifest.get("cost") is not None:
        payload["cost_hints"] = manifest["cost"]
    if changelog:
        payload["changelog_md"] = changelog
    return payload


def index_url(base_url: str) -> str:
    """Full index endpoint for ``base_url``. Pure.

    The bearer token must not travel in clear text, so plain ``http`` is only
    allowed for loopback hosts (a local API from ``make api``).
    """
    parts = urlsplit(base_url.strip())
    if parts.scheme not in ("https", "http") or not parts.hostname:
        raise ValueError(f"IACP_API_URL must be an http(s) URL, got {base_url!r}")
    if parts.scheme == "http" and parts.hostname not in _LOOPBACK_HOSTS:
        raise ValueError("IACP_API_URL must use https unless it points at localhost")
    return base_url.strip().rstrip("/") + INDEX_PATH


class _RefuseRedirects(urllib.request.HTTPRedirectHandler):
    """Never follow a redirect: urllib would replay the bearer token to the new
    location (possibly another host or plain http). The 3xx surfaces as an HTTPError."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001 (urllib API)
        return None


def build_opener() -> urllib.request.OpenerDirector:
    """An opener that refuses redirects."""
    return urllib.request.build_opener(_RefuseRedirects())


@dataclass(frozen=True)
class IndexResult:
    created: bool
    status: int
    body: dict[str, Any]


class CatalogClient:
    """Posts releases to the backend's internal catalog endpoint."""

    RETRYABLE = frozenset({429, 500, 502, 503, 504})

    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        timeout: float = 15.0,
        attempts: int = 3,
        backoff: float = 2.0,
        opener: urllib.request.OpenerDirector | None = None,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        if not token:
            raise ValueError("IACP_INTERNAL_TOKEN is empty")
        self.url = index_url(base_url)
        self._token = token
        self.timeout = timeout
        self.attempts = max(1, attempts)
        self.backoff = backoff
        self._opener = opener or build_opener()
        self._sleep = sleep

    def _request(self, payload: Mapping[str, Any]) -> urllib.request.Request:
        return urllib.request.Request(
            self.url,
            data=json.dumps(payload).encode("utf-8"),
            method="POST",
            headers={
                "Authorization": f"Bearer {self._token}",
                "Content-Type": "application/json",
                "Accept": "application/json",
                "User-Agent": "iacp-module-release",
            },
        )

    def index(self, payload: Mapping[str, Any]) -> IndexResult:
        """POST ``payload``; 201 means newly indexed, 200 means already indexed (idempotent)."""
        last_error: CatalogError | None = None
        for attempt in range(1, self.attempts + 1):
            try:
                with self._opener.open(self._request(payload), timeout=self.timeout) as resp:
                    raw = resp.read().decode("utf-8")
                    if resp.status not in (200, 201):
                        raise CatalogError(f"catalog index failed: unexpected HTTP {resp.status}", resp.status, raw)
                    return IndexResult(created=resp.status == 201, status=resp.status, body=_json(raw))
            except urllib.error.HTTPError as exc:
                body = exc.read().decode("utf-8", errors="replace")
                if 300 <= exc.code < 400:
                    location = exc.headers.get("Location", "") if exc.headers else ""
                    raise CatalogError(f"catalog index failed: HTTP {exc.code} redirect to {location!r} "
                                       "refused; set IACP_API_URL to the final URL", exc.code, body) from exc
                last_error = CatalogError(f"catalog index failed: HTTP {exc.code}: {_detail(body)}", exc.code, body)
                if exc.code not in self.RETRYABLE:
                    raise last_error from exc
            except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
                last_error = CatalogError(f"catalog index failed: {getattr(exc, 'reason', exc)}")
            if attempt < self.attempts:
                self._sleep(self.backoff * attempt)
        assert last_error is not None
        raise last_error


def _json(raw: str) -> dict[str, Any]:
    try:
        value = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        return {}
    return value if isinstance(value, dict) else {}


def _detail(body: str) -> str:
    """Short human message from an RFC 7807 problem body, or the raw text."""
    problem = _json(body)
    parts = [str(problem[k]) for k in ("title", "detail") if problem.get(k)]
    errors = problem.get("errors")
    if isinstance(errors, list):
        parts.extend(
            f"{e.get('pointer', '')} {e.get('message', '')}".strip() for e in errors if isinstance(e, dict)
        )
    return "; ".join(parts) or body[:500]
