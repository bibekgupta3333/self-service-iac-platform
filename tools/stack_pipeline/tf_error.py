"""Reduce Terraform failure output to short, redacted text that is safe to send and store.

Shared by the drift scan (``drift.summarize_error``) and apply failure reports
(``report --error-log``), so a tightening of the rules benefits both. Mirrors
``backend/internal/tferror`` (the API applies the same rules again before storing);
``backend/internal/tferror/testdata/vectors.json`` holds the cases both test suites run.
Pure.

Terraform prints a diagnostic as a box (``╷`` ... ``╵``, each line prefixed with
``│``) whose first line is ``Error: <summary>``. The lines after it carry source snippets
and provider detail, which can hold attribute values and request bodies, so only ``Error:``
lines are kept, each cut after the resource identifier (at the first ``": "`` after the
prefix), because what follows is the provider's own message, which can echo input.
Credential-shaped text is replaced as defence in depth and control characters become spaces.

What the backend needs to classify a failure travels separately as ``signals``: entries of
the fixed ``VOCABULARY`` (AWS error codes and Terraform phrases the backend's classifier is
built from) found in the ``Error:`` lines, plus ``StatusCode: <nnn>``. A signal is always one
of those fixed strings, never text copied from the output.
"""

from __future__ import annotations

import re

SECRET_RE = re.compile(
    r"((?:secret|token|password|passwd|authorization|api[_-]?key|access[_-]?key)[A-Za-z_-]*\s*[=:]\s*)(?:bearer\s+)?\S+"
    r"|AKIA[0-9A-Z]{16}|bearer\s+\S+", re.IGNORECASE)
_ERROR_LINE_RE = re.compile(r"Error:\s+\S")
_STATUS_RE = re.compile(r"\bstatus ?code:? ?([1-5][0-9]{2})\b", re.IGNORECASE)
_STATUS_SIGNAL_RE = re.compile(r"StatusCode: [1-5][0-9]{2}")
_BOX = " \t│╷╵"
MAX_SIGNALS = 12

# The failure vocabulary, in the backend's order (tferror.Vocabulary, where each entry is
# tagged with the class and reason recovery gives it). The shared vectors pin this list.
VOCABULARY = (
    "role cannot be assumed", "role defined for the function cannot be assumed", "InvalidInstanceProfile",
    "Invalid IAM Instance Profile", "ThrottlingException", "Throttling", "RequestLimitExceeded",
    "TooManyRequestsException", "Rate exceeded", "SlowDown", "ProvisionedThroughputExceeded", "RequestThrottled",
    "PriorRequestNotComplete", "InternalError", "InternalFailure", "InternalServerError", "ServiceUnavailable",
    "ServiceFailure", "Bad Gateway", "Service Unavailable", "Gateway Timeout", "i/o timeout",
    "context deadline exceeded", "TLS handshake timeout", "connection reset by peer", "connection refused",
    "no such host", "RequestTimeout", "request canceled", "unexpected EOF", "use of closed network connection",
    "Error acquiring the state lock", "Error locking state", "ConditionalCheckFailedException", "PreconditionFailed",
    "runner has received a shutdown signal", "lost communication with the server",
    "The hosted runner encountered an error",
    "plan_mismatch", "differs from the approved plan", "plan hash mismatch", "Invalid value for variable",
    "Invalid value for input", "Unsupported argument", "Missing required argument", "Reference to undeclared",
    "Unsupported attribute", "Invalid reference", "InvalidParameterCombination", "InvalidParameterValue",
    "ValidationException", "ValidationError", "MalformedPolicyDocument", "InvalidRequest", "AlreadyExists",
    "already exists", "ResourceInUseException", "DuplicateRecord", "ResourceConflictException",
    "LimitExceeded", "InsufficientInstanceCapacity", "QuotaExceeded", "TooManyBuckets", "ServiceQuota",
    "AccessDenied", "UnauthorizedOperation", "is not authorized to perform", "explicit deny", "AuthorizationError",
    "InvalidClientTokenId",
)


def redact(line: str) -> str:
    """Credential-shaped text replaced by ``<redacted>``, control characters by spaces."""
    line = "".join(" " if ord(c) < 0x20 or ord(c) == 0x7F else c for c in line)

    def sub(m: re.Match[str]) -> str:
        return (m.group(1).rstrip() + " <redacted>") if m.group(1) else "<redacted>"

    return SECRET_RE.sub(sub, line)


def cut_after_identifier(line: str) -> str:
    """``Error: putting SSM Parameter (/app/db): ...`` becomes ``Error: putting SSM Parameter (/app/db)``.
    Idempotent."""
    if not line.startswith("Error:"):
        return line
    cut = line.find(": ", len("Error:"))
    return line[:cut].rstrip() if cut >= 0 else line


def _full_error_lines(text: str, limit: int) -> list[str]:
    out: list[str] = []
    for raw in text.splitlines():
        if len(out) >= limit:
            break
        line = raw.strip().strip(_BOX).strip()
        if _ERROR_LINE_RE.match(line):
            out.append(redact(line))
    return out


def error_lines(text: str, limit: int) -> list[str]:
    """Up to ``limit`` redacted ``Error:`` lines, cut after the resource identifier."""
    return [cut_after_identifier(line) for line in _full_error_lines(text, limit)]


def cap(text: str, limit: int) -> str:
    return text if len(text) <= limit else text[: limit - 3] + "..."


def summary(text: str, max_lines: int, limit: int, fallback: str) -> str:
    """The ``Error:`` lines, one per line, capped; ``fallback`` when there is none.
    A summary of a summary is the same summary."""
    lines = error_lines(text, max_lines)
    return cap("\n".join(lines), limit) if lines else fallback


def signals(text: str) -> list[str]:
    """``VOCABULARY`` entries found (case-insensitively) in the ``Error:`` lines, in vocabulary
    order, then ``StatusCode: <nnn>``; no duplicates, at most ``MAX_SIGNALS``."""
    text = "\n".join(_full_error_lines(text, 16))
    lower = text.lower()
    out: list[str] = []

    def add(token: str) -> None:
        if len(out) < MAX_SIGNALS and token not in out:
            out.append(token)

    for v in VOCABULARY:
        if v.lower() in lower:
            add(v)
    for m in _STATUS_RE.finditer(text):
        add("StatusCode: " + m.group(1))
    return out


def filter_signals(reported: list[str]) -> list[str]:
    """The reported signals that are vocabulary entries or HTTP statuses (mirrors FilterSignals)."""
    out: list[str] = []
    for s in reported:
        if len(out) >= MAX_SIGNALS:
            break
        if (s in VOCABULARY or _STATUS_SIGNAL_RE.fullmatch(s)) and s not in out:
            out.append(s)
    return out
