"""Run reports for ``POST /v1/runs`` (docs/03 section 6.9, WBS 5.6).

Everything here is pure. ``build_report`` mirrors ``runs.Report`` and
``runs.Validate`` in backend/internal/runs/report.go: the backend decodes
strictly (unknown top-level fields are rejected), so only the fields it
declares are sent, and a report it would reject with 422 fails here first.
The HTTP and OIDC adapters live in ``api_client``.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Any, Mapping, Sequence
from urllib.parse import urlsplit

# Pipelines and the stage statuses runs.Validate accepts.
PIPELINES = ("validate", "apply", "drift", "ttl_destroy")
STATUSES = ("in_progress", "passed", "failed", "cancelled")
# A passed "summary" stage finishes the run as succeeded (runs.FinalStage).
FINAL_STAGE = "summary"
SENSITIVE = "<sensitive>"

# GitHub step outcomes (steps.<id>.outcome / job.status) to stage statuses.
_OUTCOMES = {"success": "passed", "failure": "failed", "cancelled": "cancelled", "skipped": "cancelled"}

# Same patterns as backend/internal/runs/report.go.
_STACK_PATH_RE = re.compile(r"environments/[a-z]+/[a-z][a-z0-9-]{1,38}/[a-z][a-z0-9-]{1,40}")
_SHORT_ID_RE = re.compile(r"req-[0-9a-z]{4,32}")
_SHA_RE = re.compile(r"[0-9a-f]{40}")
_STAGE_RE = re.compile(r"[a-z][a-z0-9_-]{0,39}")
_LOOPBACK_HOSTS = frozenset({"localhost", "127.0.0.1", "::1"})
MAX_ERROR_MESSAGE = 2_000


class ReportError(ValueError):
    """The report would be rejected by the API, or its inputs are unusable."""


def needs_lock(pipeline: str) -> bool:
    """Mirror of runs.Kind.NeedsLock: every pipeline but drift carries a fencing token."""
    return pipeline != "drift"


def stage_status(value: str) -> str:
    """A stage status, or a GitHub outcome mapped to one. Pure.

    ``skipped`` becomes ``cancelled``: the stage did not run because an earlier
    one failed or the job was cancelled.
    """
    value = value.strip().lower()
    if value in STATUSES:
        return value
    if value in _OUTCOMES:
        return _OUTCOMES[value]
    raise ReportError(f"status must be one of {', '.join(STATUSES)} or a GitHub outcome, got {value!r}")


@dataclass(frozen=True)
class GitHubRun:
    """The reporting workflow run. ``head_sha`` is always the job's ``github.sha``."""

    run_id: int
    run_attempt: int
    head_sha: str
    job_id: int | None = None
    log_url: str = ""

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> GitHubRun:
        """Read the runner's default variables. Pure over the mapping.

        GITHUB_SHA is the commit the OIDC token's ``sha`` claim names. For
        pull_request events that is the merge commit, not the PR head, and the
        API answers 403 run_mismatch for any other value, so there is no override.
        """
        try:
            run_id = int(env.get("GITHUB_RUN_ID", ""))
            attempt = int(env.get("GITHUB_RUN_ATTEMPT", ""))
        except ValueError as exc:
            raise ReportError("GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT must be set (run inside GitHub Actions)") from exc
        server, repo = env.get("GITHUB_SERVER_URL", ""), env.get("GITHUB_REPOSITORY", "")
        log_url = f"{server}/{repo}/actions/runs/{run_id}/attempts/{attempt}" if server and repo else ""
        return cls(run_id=run_id, run_attempt=attempt, head_sha=env.get("GITHUB_SHA", "").strip(), log_url=log_url)

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"run_id": self.run_id, "run_attempt": self.run_attempt, "head_sha": self.head_sha}
        if self.job_id:
            out["job_id"] = self.job_id
        return out


@dataclass(frozen=True)
class RunReport:
    """One stage report. ``plan``, ``policy``, ``outputs`` and ``error`` are JSON values."""

    pipeline: str
    stack_path: str
    stage: str
    status: str
    github: GitHubRun
    request_short_id: str = ""
    fencing_token: int | None = None
    plan: Mapping[str, Any] | None = None
    policy: Mapping[str, Any] | None = None
    outputs: Mapping[str, Any] | None = None
    terraform_exit_code: int | None = None
    error: Mapping[str, Any] | None = None

    def problems(self) -> list[str]:
        """What runs.Validate would reject, as ``/pointer: message`` lines. Pure."""
        errs: list[str] = []
        if self.pipeline not in PIPELINES:
            errs.append(f"/pipeline: must be one of {', '.join(PIPELINES)}")
        if not _STACK_PATH_RE.fullmatch(self.stack_path):
            errs.append("/stack_path: must look like environments/<env>/<team>/<stack>")
        if self.request_short_id and not _SHORT_ID_RE.fullmatch(self.request_short_id):
            errs.append("/request_short_id: must look like req-<id>")
        if self.github.run_id <= 0:
            errs.append("/github/run_id: must be a positive integer")
        if self.github.run_attempt <= 0:
            errs.append("/github/run_attempt: must be a positive integer")
        if not _SHA_RE.fullmatch(self.github.head_sha):
            errs.append("/github/head_sha: must be 40 lowercase hex characters (the job's github.sha)")
        if not _STAGE_RE.fullmatch(self.stage):
            errs.append("/stage: must be a short lowercase stage name")
        if self.status not in STATUSES:
            errs.append(f"/status: must be one of {', '.join(STATUSES)}")
        if needs_lock(self.pipeline) and (self.fencing_token is None or self.fencing_token <= 0):
            errs.append(f"/fencing_token: {self.pipeline} reports must carry the stack lock's fencing token")
        if needs_lock(self.pipeline) and not self.request_short_id:
            errs.append(f"/request_short_id: {self.pipeline} reports must name their request")
        return errs

    def to_payload(self) -> dict[str, Any]:
        """The POST /v1/runs body; raises ReportError when the API would reject it. Pure."""
        errs = self.problems()
        if errs:
            raise ReportError("run report is invalid: " + "; ".join(errs))
        body: dict[str, Any] = {
            "pipeline": self.pipeline,
            "stack_path": self.stack_path,
            "github": self.github.to_dict(),
            "stage": self.stage,
            "status": self.status,
        }
        optional = {
            "request_short_id": self.request_short_id or None,
            "fencing_token": self.fencing_token,
            "log_url": self.github.log_url or None,
            "plan": self.plan,
            "policy": self.policy,
            "outputs": self.outputs,
            "terraform_exit_code": self.terraform_exit_code,
            "error": self.error,
        }
        body.update({k: v for k, v in optional.items() if v is not None})
        return body


# ---- payload blocks built from the pipeline's own files ---------------------------


def plan_block(summary: Mapping[str, Any]) -> dict[str, Any] | None:
    """The ``plan`` object from a ``summary --json-out`` document, or None without a plan. Pure.

    Counts use the docs/03 names: add, change, destroy, replace.
    """
    plan_hash = summary.get("plan_hash")
    if not plan_hash:
        return None
    counts = summary.get("counts") or {}
    return {
        "hash": plan_hash,
        "counts": {
            "add": int(counts.get("create", 0)),
            "change": int(counts.get("update", 0)),
            "destroy": int(counts.get("delete", 0)),
            "replace": int(counts.get("replace", 0)),
        },
        "resource_changes": [
            {"address": c["address"], "actions": [c["action"]]}
            for c in summary.get("changes") or []
            if isinstance(c, Mapping) and "address" in c and "action" in c
        ],
    }


def policy_block(summary: Mapping[str, Any]) -> dict[str, Any] | None:
    """The ``policy`` object (OPA deny and warn messages) from a summary document. Pure."""
    policy = summary.get("policy")
    if not isinstance(policy, Mapping):
        return None
    return {"opa": {"deny": list(policy.get("failures") or []), "warn": list(policy.get("warnings") or [])}}


def summary_status(summary: Mapping[str, Any] | None) -> str:
    """``passed`` when the summary says every step and policy passed, else ``failed``. Pure."""
    return "passed" if summary is not None and summary.get("ok") is True else "failed"


def redact_outputs(outputs: Mapping[str, Any]) -> dict[str, Any]:
    """``terraform output -json`` with sensitive values replaced by ``<sensitive>``. Pure.

    Each entry is ``{"sensitive": bool, "type": ..., "value": ...}``. An entry
    that is not shaped like that is treated as sensitive, so nothing unknown leaks.
    """
    out: dict[str, Any] = {}
    for name, entry in sorted(outputs.items()):
        if isinstance(entry, Mapping) and entry.get("sensitive") is False and "value" in entry:
            out[name] = entry["value"]
        else:
            out[name] = SENSITIVE
    return out


def error_block(message: str, error_class: str = "permanent") -> dict[str, Any] | None:
    """The ``error`` object for a failed stage, or None for an empty message. Pure."""
    message = message.strip()
    if not message:
        return None
    if error_class not in ("transient", "permanent"):
        raise ReportError(f"error class must be transient or permanent, got {error_class!r}")
    if len(message) > MAX_ERROR_MESSAGE:
        message = message[:MAX_ERROR_MESSAGE] + " ... (truncated)"
    return {"class": error_class, "message": message}


def api_base_url(value: str) -> str:
    """Validated API base URL without a trailing slash. Pure.

    The bearer token must not travel in clear text, so plain http is allowed
    only for loopback hosts (a local API from ``make api``).
    """
    parts = urlsplit(value.strip())
    if parts.scheme not in ("https", "http") or not parts.hostname:
        raise ReportError(f"IACP_API_URL must be an http(s) URL, got {value!r}")
    if parts.scheme == "http" and parts.hostname not in _LOOPBACK_HOSTS:
        raise ReportError("IACP_API_URL must use https unless it points at localhost")
    if parts.query or parts.fragment:
        raise ReportError("IACP_API_URL must not carry a query or fragment")
    return value.strip().rstrip("/")


def describe_problem(body: Mapping[str, Any], raw: str = "") -> str:
    """Short message from an RFC 7807 problem body, or the raw text. Pure."""
    parts = [str(body[k]) for k in ("code", "title", "detail") if body.get(k)]
    errors = body.get("errors")
    if isinstance(errors, Sequence) and not isinstance(errors, str):
        parts.extend(
            f"{e.get('pointer', '')} {e.get('message', '')}".strip() for e in errors if isinstance(e, Mapping)
        )
    return "; ".join(parts) or raw[:500]
