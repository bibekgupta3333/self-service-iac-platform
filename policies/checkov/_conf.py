"""Helpers for reading Checkov's parsed HCL resource configuration.

Checkov wraps every attribute value in a single-element list, e.g.
``{"tags": [{"iacp:team": "payments"}], "tracing_config": [{"mode": ["Active"]}]}``.
"""

from __future__ import annotations

from typing import Any


def first(conf: dict[str, Any], key: str, default: Any = None) -> Any:
    """Return the unwrapped value of ``key``, or ``default`` when absent."""
    value = conf.get(key)
    if isinstance(value, list):
        return value[0] if value else default
    return default if value is None else value


def blocks(conf: dict[str, Any], key: str) -> list[dict[str, Any]]:
    """Return the nested blocks stored under ``key`` (always a list of dicts)."""
    value = conf.get(key) or []
    if isinstance(value, dict):
        value = [value]
    return [b for b in value if isinstance(b, dict)]


UNRESOLVED_PREFIXES = ("var.", "local.", "module.", "data.", "each.", "count.")


def is_unresolved(value: Any) -> bool:
    """True when Checkov could not evaluate an expression and kept its source text."""
    return isinstance(value, str) and ("${" in value or value.startswith(UNRESOLVED_PREFIXES))
