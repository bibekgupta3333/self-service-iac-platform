"""Reduce Terraform failure output to short, redacted text that is safe to send and store.

Shared by the drift scan (``drift.summarize_error``) and apply failure reports
(``report --error-log``), so a tightening of the rules benefits both. Mirrors
``backend/internal/tferror`` (the API applies the same rules again before storing). Pure.

Terraform prints a diagnostic as a box (``╷`` ... ``╵``, each line prefixed with
``│``) whose first line is ``Error: <summary>``. The lines after it carry source snippets
and provider detail, which can hold attribute values and request bodies, so only ``Error:``
lines are kept, each cut after the resource identifier (at the first ``": "`` after the
prefix), because what follows is the provider's own message, which can echo input.
Credential-shaped text is replaced as defence in depth and control characters become spaces.

What the backend needs to classify a failure (an AWS error code, an HTTP status, a known
Terraform phrase) travels separately as ``signals``: tokens matched against a fixed
vocabulary in the ``Error:`` lines, never free text.
"""

from __future__ import annotations

import re

SECRET_RE = re.compile(
    r"((?:secret|token|password|passwd|authorization|api[_-]?key|access[_-]?key)[A-Za-z_-]*\s*[=:]\s*)(?:bearer\s+)?\S+"
    r"|AKIA[0-9A-Z]{16}|bearer\s+\S+", re.IGNORECASE)
_ERROR_LINE_RE = re.compile(r"Error:\s+\S")
_BOX = " \t│╷╵"

_API_ERROR_RE = re.compile(r"\bapi error ([A-Z][A-Za-z0-9]{2,63})\b")
_CODE_RE = re.compile(
    r"\b([A-Z][A-Za-z0-9]{2,63}(?:Exception|Exceeded|Error|Failure|Denied|Unavailable|AlreadyExists|Throttling|Capacity))\b")
_STATUS_RE = re.compile(r"\bstatus ?code:? ?([1-5][0-9]{2})\b", re.IGNORECASE)
PHRASES = (
    "Error acquiring the state lock", "Error locking state", "i/o timeout", "context deadline exceeded",
    "TLS handshake timeout", "connection reset by peer", "connection refused", "no such host",
    "Rate exceeded", "SlowDown", "Throttling", "role cannot be assumed",
    "role defined for the function cannot be assumed", "runner has received a shutdown signal",
    "lost communication with the server", "plan_mismatch", "differs from the approved plan",
    "Invalid value for variable", "Unsupported argument", "Missing required argument",
    "Reference to undeclared", "Unsupported attribute", "Invalid reference", "already exists",
    "is not authorized to perform", "explicit deny",
)
MAX_SIGNALS = 8


def redact(line: str) -> str:
    """Credential-shaped text replaced by ``<redacted>``, control characters by spaces."""
    line = "".join(" " if ord(c) < 0x20 or ord(c) == 0x7F else c for c in line)

    def sub(m: re.Match[str]) -> str:
        return (m.group(1).rstrip() + " <redacted>") if m.group(1) else "<redacted>"

    return SECRET_RE.sub(sub, line)


def cut_after_identifier(line: str) -> str:
    """``Error: putting SSM Parameter (/app/db): ...`` becomes ``Error: putting SSM Parameter (/app/db)``."""
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
    """The ``Error:`` lines joined with `` | ``, capped; ``fallback`` when there is none."""
    lines = error_lines(text, max_lines)
    return cap(" | ".join(lines), limit) if lines else fallback


def signals(text: str) -> list[str]:
    """Classification tokens from the ``Error:`` lines: ``api error <Code>``,
    ``StatusCode: <nnn>``, AWS-style code names and the fixed ``PHRASES``. No input values."""
    text = "\n".join(_full_error_lines(text, 16))
    out: list[str] = []

    def add(token: str) -> None:
        if len(out) < MAX_SIGNALS and token not in out:
            out.append(token)

    for m in _API_ERROR_RE.finditer(text):
        add("api error " + m.group(1))
    for m in _STATUS_RE.finditer(text):
        add("StatusCode: " + m.group(1))
    for m in _CODE_RE.finditer(text):
        add(m.group(1))
    lower = text.lower()
    for p in PHRASES:
        if p.lower() in lower:
            add(p)
    return out


def excerpt(text: str, max_lines: int, limit: int, fallback: str) -> str:
    """``summary`` plus the ``signals`` in brackets, capped at ``limit``. Mirrors tferror.Excerpt."""
    s = summary(text, max_lines, limit, fallback)
    sig = signals(text)
    if sig:
        s += " [" + "; ".join(sig) + "]"
    return cap(s, limit)
