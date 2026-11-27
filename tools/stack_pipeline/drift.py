"""Drift checks for ``POST /v1/internal/drift`` (docs/06 section 4, WBS 8.3).

Everything here is pure. ``parse_refresh_plan`` reads ``terraform show -json`` of a
``plan -refresh-only`` plan, ``build_outcome`` classifies a stack's check and
``DriftOutcome.problems`` mirrors ``drift.Validate`` in backend/internal/drift/classify.go
(the backend decodes strictly and refuses a report whose parts disagree, so a report it
would reject with 422 fails here first). The HTTP adapter is ``IacpApiClient.post_drift``
in ``api_client``; the CLI is ``drift`` in ``cli``.

Only addresses, resource types, actions and the *names* of changed attributes leave the
plan: attribute values never do (they may be sensitive), and the plan hash covers the
names, not the values.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass, field
from typing import Any, Mapping, Sequence

from . import tf_error
from .report import GitHubRun, ReportError

# Terraform's `plan -detailed-exitcode` outcomes.
EXIT_CLEAN, EXIT_ERROR, EXIT_CHANGES = 0, 1, 2
RESULTS = ("clean", "drifted", "error")
# Same limits as backend/internal/drift/classify.go.
MAX_CHANGED = 200
MAX_ERROR = 1000
MAX_ACTIONS = 4
MAX_TYPE = 128

_STACK_PATH_RE = re.compile(r"environments/[a-z]+/[a-z][a-z0-9-]{1,38}/[a-z][a-z0-9-]{1,40}")
_SHA_RE = re.compile(r"[0-9a-f]{40}")
_HASH_RE = re.compile(r"[0-9a-f]{64}")
_ADDRESS_RE = re.compile(r"[A-Za-z0-9_.\[\]\"'/:-]{1,512}")
_ACTION_RE = re.compile(r"[a-z-]{1,16}")
_OUTCOME_KEYS = frozenset({"stack_path", "result", "terraform_exit_code", "summary", "changed", "plan_hash", "error"})


def classify_exit(code: int, drifted: int) -> str:
    """The result of ``terraform plan -refresh-only -detailed-exitcode``. Pure.

    0 is clean, 1 is a failed check (not drift), 2 is drift. A refresh-only plan also
    exits 2 when only output values would change; with no drifted resource that is not
    drift, so it is clean. Mirrors ``drift.ClassifyExit`` in the backend.
    """
    if code == EXIT_CLEAN:
        return "clean"
    if code == EXIT_ERROR:
        return "error"
    if code == EXIT_CHANGES:
        return "drifted" if drifted > 0 else "clean"
    raise ReportError(f"terraform exit code must be 0, 1 or 2 (plan -detailed-exitcode), got {code}")


@dataclass(frozen=True)
class Finding:
    """One drifted resource of a refresh-only plan."""

    address: str
    type: str
    actions: tuple[str, ...]
    attributes: tuple[str, ...] = ()  # names of the attributes that differ; never values

    def to_dict(self) -> dict[str, Any]:
        return {"address": self.address, "type": self.type, "actions": list(self.actions)}


def _changed_attributes(change: Mapping[str, Any]) -> tuple[str, ...]:
    before, after = change.get("before"), change.get("after")
    if not isinstance(before, Mapping) or not isinstance(after, Mapping):
        return ()  # a deleted resource has no after
    return tuple(sorted(k for k in set(before) | set(after) if before.get(k) != after.get(k)))


def parse_refresh_plan(plan: Mapping[str, Any]) -> list[Finding]:
    """The drifted resources of ``terraform show -json`` of a refresh-only plan. Pure.

    ``resource_drift`` lists what changed outside Terraform. An entry whose only action is
    ``no-op`` is not drift. A malformed entry raises: a plan this tool cannot read must
    not be reported as clean.
    """
    if not isinstance(plan, Mapping):
        raise ReportError("the plan must be a JSON object (terraform show -json)")
    entries = plan.get("resource_drift") or []
    if not isinstance(entries, Sequence) or isinstance(entries, (str, bytes)):
        raise ReportError("plan resource_drift must be a list")
    out: list[Finding] = []
    for i, entry in enumerate(entries):
        if not isinstance(entry, Mapping):
            raise ReportError(f"plan resource_drift[{i}] must be an object")
        change = entry.get("change")
        actions = change.get("actions") if isinstance(change, Mapping) else None
        address = entry.get("address")
        if not isinstance(address, str) or not isinstance(actions, list) or not all(isinstance(a, str) for a in actions):
            raise ReportError(f"plan resource_drift[{i}] has no address or actions")
        if actions == ["no-op"]:
            continue
        out.append(Finding(address=address, type=str(entry.get("type", "")), actions=tuple(actions),
                           attributes=_changed_attributes(change)))
    return sorted(out, key=lambda f: f.address)


def drift_hash(findings: Sequence[Finding]) -> str:
    """Fingerprint of the drift: addresses, actions and the names of the changed attributes.

    The same drift hashes the same on every night, so a repeated finding is recognisable;
    it carries no attribute value. Pure.
    """
    doc = [{"address": f.address, "actions": list(f.actions), "attributes": list(f.attributes)} for f in findings]
    return hashlib.sha256(json.dumps(doc, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def counts(findings: Sequence[Finding]) -> dict[str, int]:
    """``drifted`` (every resource), ``updated`` and ``deleted`` (a resource is counted once). Pure."""
    deleted = sum(1 for f in findings if "delete" in f.actions)
    updated = sum(1 for f in findings if "delete" not in f.actions and "update" in f.actions)
    return {"drifted": len(findings), "updated": updated, "deleted": deleted}


MAX_SUMMARY = 200
GENERIC_FAILURE = "terraform plan -refresh-only failed; see the workflow log"


# The messages this tool and the workflow write themselves (not terraform's output): fixed
# text with at most a number and no terraform or provider text, and no "Error:" prefix.
# An explicit allow-list; anything else is terraform output and is reduced to its Error line.
# Keep in step with platformMessages in backend/internal/drift/classify.go.
_PLATFORM_MESSAGE_RE = re.compile(
    r"(?:(?:module pins|terraform init|installed module verification|the plan step) failed before the refresh-only plan could run"
    r"|terraform exited with the unexpected code \d{1,4}"
    r"|terraform exited \d{1,4} but wrote no readable plan JSON"
    r"|terraform plan -refresh-only failed; see the workflow log"
    r"|the plan must be a JSON object \(terraform show -json\)"
    r"|plan resource_drift must be a list"
    r"|plan resource_drift\[\d{1,6}\] (?:must be an object|has no address or actions))")


def summarize_error(text: str) -> str:
    """One redacted line of a failure, at most ``MAX_SUMMARY`` characters. Pure.

    A message the scan itself wrote (``_PLATFORM_MESSAGE_RE``, an explicit allow-list) passes
    unchanged. Everything else is terraform's output and is reduced as follows.

    Terraform prints a diagnostic as a box (``\u2577`` ... ``\u2575``, each line prefixed with
    ``\u2502``) whose first line is ``Error: <summary>``; the lines after it carry source
    snippets and provider detail, which can hold attribute values and request bodies. Only the
    first line that starts with ``Error:`` (after the box characters) is kept, cut after the
    resource identifier (at the first ``": "`` after the prefix, so ``Error: putting SSM
    Parameter (/app/db)``), because what follows is the provider's own message, which can echo
    input; with none, the
    fixed ``GENERIC_FAILURE`` is used, never "the first non-empty line" (on a full log that is
    a "Refreshing state" line, on a tail it is source text). Credential-shaped text is replaced
    as defence in depth and control characters become spaces (``tf_error``, shared with apply
    failure reports). Mirrors ``drift.SummarizeError`` in the backend.
    """
    if _PLATFORM_MESSAGE_RE.fullmatch(text.strip()):
        return text.strip()
    return tf_error.summary(text, 1, MAX_SUMMARY, GENERIC_FAILURE)


def one_line(text: str, limit: int = MAX_ERROR) -> str:
    """A single-line, bounded message (it ends up in an annotation and the audit trail). Pure."""
    flat = " ".join(text.split())
    suffix = " ... (truncated)"
    return flat if len(flat) <= limit else flat[: limit - len(suffix)] + suffix


@dataclass(frozen=True)
class DriftOutcome:
    """One stack's drift check, before the workflow run is known."""

    stack_path: str
    result: str
    terraform_exit_code: int
    summary: Mapping[str, int] = field(default_factory=lambda: {"drifted": 0, "updated": 0, "deleted": 0})
    changed: tuple[Finding, ...] = ()
    plan_hash: str = ""
    error: str = ""

    def problems(self, github: GitHubRun | None = None) -> list[str]:
        """What ``drift.Validate`` would reject, as ``/pointer: message`` lines. Pure."""
        errs: list[str] = []
        s = self.summary
        if not _STACK_PATH_RE.fullmatch(self.stack_path):
            errs.append("/stack_path: must look like environments/<env>/<team>/<stack>")
        if github is not None:
            if github.run_id <= 0:
                errs.append("/github/run_id: must be a positive integer")
            if github.run_attempt <= 0:
                errs.append("/github/run_attempt: must be a positive integer")
            if not _SHA_RE.fullmatch(github.head_sha):
                errs.append("/github/head_sha: must be 40 lowercase hex characters (the job's github.sha)")
        try:
            want = classify_exit(self.terraform_exit_code, s.get("drifted", 0))
        except ReportError as exc:
            errs.append(f"/terraform_exit_code: {exc}")
        else:
            if self.result != want:
                errs.append(f"/result: exit code {self.terraform_exit_code} with {s.get('drifted', 0)} drifted "
                            f"resources means {want!r}, not {self.result!r}")
        if self.plan_hash and not _HASH_RE.fullmatch(self.plan_hash):
            errs.append("/plan_hash: must be 64 lowercase hex characters")
        if len(self.error) > MAX_ERROR:
            errs.append(f"/error: must be at most {MAX_ERROR} characters")
        if any(not isinstance(s.get(k), int) or isinstance(s.get(k), bool) or s[k] < 0 for k in ("drifted", "updated", "deleted")):
            errs.append("/summary: drifted, updated and deleted must be non-negative integers")
        elif self.result == "drifted":
            if s["drifted"] <= 0:
                errs.append("/summary/drifted: a drifted report must count at least one drifted resource")
            if s["updated"] + s["deleted"] > s["drifted"]:
                errs.append("/summary: updated and deleted cannot exceed drifted")
            if not self.plan_hash:
                errs.append("/plan_hash: a drifted report must carry the plan hash")
        elif any(s.values()) or self.changed:
            errs.append(f"/summary: a {self.result} report carries no drifted resources")
        if self.result == "error" and not self.error.strip():
            errs.append("/error: an error report must say what failed")
        if self.result != "error" and self.error:
            errs.append("/error: only an error report carries an error")
        if len(self.changed) > MAX_CHANGED:
            errs.append(f"/changed: at most {MAX_CHANGED} changed resources")
        for i, c in enumerate(self.changed):
            if not _ADDRESS_RE.fullmatch(c.address):
                errs.append(f"/changed/{i}/address: must be a terraform resource address")
            if len(c.type) > MAX_TYPE:
                errs.append(f"/changed/{i}/type: must be at most {MAX_TYPE} characters")
            if not 0 < len(c.actions) <= MAX_ACTIONS or not all(_ACTION_RE.fullmatch(a) for a in c.actions):
                errs.append(f"/changed/{i}/actions: must list one to {MAX_ACTIONS} terraform actions")
        return errs

    def to_dict(self) -> dict[str, Any]:
        """The result file the scan job writes (and ``outcome_from_dict`` reads). Pure."""
        out: dict[str, Any] = {
            "stack_path": self.stack_path,
            "result": self.result,
            "terraform_exit_code": self.terraform_exit_code,
            "summary": dict(self.summary),
            "changed": [c.to_dict() for c in self.changed],
        }
        if self.plan_hash:
            out["plan_hash"] = self.plan_hash
        if self.error:
            out["error"] = self.error
        return out

    def to_payload(self, github: GitHubRun) -> dict[str, Any]:
        """The ``POST /v1/internal/drift`` body; raises ReportError when the API would refuse it. Pure."""
        errs = self.problems(github)
        if errs:
            raise ReportError("drift report is invalid: " + "; ".join(errs))
        body = self.to_dict()
        body["github"] = github.to_dict()
        if github.log_url:
            body["log_url"] = github.log_url
        return body


def build_outcome(
    stack_path: str,
    exit_code: int,
    plan: Mapping[str, Any] | None = None,
    error_message: str = "",
) -> DriftOutcome:
    """Classify one stack's refresh-only check. Pure.

    Exit 1 is a failed check: the stack's state is unknown, so it is reported as an error and
    never as drift. Exit 0 and 2 need the plan JSON; a missing or unreadable plan turns the
    check into an error rather than a guess. At most ``MAX_CHANGED`` resources are listed;
    the counts and the hash always cover all of them.
    """
    if exit_code == EXIT_ERROR:
        return _error(stack_path, exit_code, summarize_error(error_message) or GENERIC_FAILURE)
    if exit_code not in (EXIT_CLEAN, EXIT_CHANGES):
        return _error(stack_path, EXIT_ERROR, f"terraform exited with the unexpected code {exit_code}")
    if plan is None:
        return _error(stack_path, EXIT_ERROR, f"terraform exited {exit_code} but wrote no readable plan JSON")
    try:
        findings = parse_refresh_plan(plan)
    except ReportError as exc:
        return _error(stack_path, EXIT_ERROR, str(exc))
    result = classify_exit(exit_code, len(findings))
    if result == "clean":
        return DriftOutcome(stack_path=stack_path, result="clean", terraform_exit_code=exit_code)
    return DriftOutcome(stack_path=stack_path, result="drifted", terraform_exit_code=exit_code, summary=counts(findings),
                        changed=tuple(findings[:MAX_CHANGED]), plan_hash=drift_hash(findings))


def _error(stack_path: str, exit_code: int, message: str) -> DriftOutcome:
    return DriftOutcome(stack_path=stack_path, result="error", terraform_exit_code=exit_code, error=one_line(message))


def outcome_from_dict(doc: Any) -> DriftOutcome:
    """Read a scan result file back. Strict: it crossed a job boundary, so it is data, never
    trusted. Unknown keys, wrong types and an inconsistent outcome are refused. Pure."""
    if not isinstance(doc, Mapping):
        raise ReportError("drift result must be a JSON object")
    extra = set(doc) - _OUTCOME_KEYS
    if extra:
        raise ReportError(f"drift result has unknown fields: {', '.join(sorted(map(str, extra)))}")
    summary, changed = doc.get("summary"), doc.get("changed")
    if not isinstance(summary, Mapping) or set(summary) != {"drifted", "updated", "deleted"}:
        raise ReportError("drift result summary must have drifted, updated and deleted")
    if not isinstance(changed, list):
        raise ReportError("drift result changed must be a list")
    findings: list[Finding] = []
    for i, c in enumerate(changed):
        if not isinstance(c, Mapping) or set(c) != {"address", "type", "actions"} or not isinstance(c["actions"], list) \
                or not isinstance(c["address"], str) or not isinstance(c["type"], str) \
                or not all(isinstance(a, str) for a in c["actions"]):
            raise ReportError(f"drift result changed[{i}] must have address, type and actions")
        findings.append(Finding(address=c["address"], type=c["type"], actions=tuple(c["actions"])))
    if any(not isinstance(v, int) or isinstance(v, bool) for v in summary.values()):
        raise ReportError("drift result summary counts must be integers")
    code, result = doc.get("terraform_exit_code"), doc.get("result")
    if not isinstance(code, int) or isinstance(code, bool) or result not in RESULTS:
        raise ReportError("drift result needs an integer terraform_exit_code and a result of clean, drifted or error")
    outcome = DriftOutcome(
        stack_path=str(doc.get("stack_path", "")), result=result, terraform_exit_code=code,
        summary={k: summary[k] for k in ("drifted", "updated", "deleted")}, changed=tuple(findings),
        plan_hash=str(doc.get("plan_hash") or ""), error=str(doc.get("error") or ""),
    )
    errs = outcome.problems()
    if errs:
        raise ReportError("drift result is invalid: " + "; ".join(errs))
    return outcome
