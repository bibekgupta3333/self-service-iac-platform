"""Pure functions for comparing manifest and Terraform interfaces."""

from __future__ import annotations

from typing import Any, Iterable, Mapping

from jsonschema import Draft202012Validator

# Terraform type family -> JSON Schema types it can accept.
_COMPATIBLE: Mapping[str, frozenset[str]] = {
    "string": frozenset({"string"}),
    "number": frozenset({"number", "integer"}),
    "bool": frozenset({"boolean"}),
    "map": frozenset({"object"}),
    "list": frozenset({"array"}),
}


def key_diff(left: Iterable[str], right: Iterable[str]) -> tuple[list[str], list[str]]:
    """Return (only in left, only in right), each sorted."""
    a, b = set(left), set(right)
    return sorted(a - b), sorted(b - a)


def json_types(prop: Mapping[str, Any]) -> frozenset[str]:
    """Normalise a JSON Schema ``type`` (string or list) to a set, without ``null``."""
    raw = prop.get("type", [])
    types = {raw} if isinstance(raw, str) else set(raw)
    return frozenset(types - {"null"})


def types_compatible(tf_family: str | None, schema_types: frozenset[str]) -> bool:
    """True if a Terraform variable type family can hold the JSON Schema types."""
    if tf_family in (None, "any"):
        return True
    allowed = _COMPATIBLE.get(tf_family)
    return bool(allowed) and bool(schema_types) and schema_types <= allowed


def schema_errors(schema: Mapping[str, Any], value: Any) -> list[str]:
    """Messages for every way ``value`` violates ``schema`` (empty when valid). Pure: no I/O."""
    validator = Draft202012Validator(dict(schema))
    return sorted(error.message for error in validator.iter_errors(value))


def format_names(names: Iterable[str]) -> str:
    """Render a list of names for a human message."""
    return ", ".join(f"'{n}'" for n in names)


def json_path(parts: Iterable[Any]) -> str:
    """Render a jsonschema error path as ``a.b[0].c``."""
    out = ""
    for part in parts:
        out += f"[{part}]" if isinstance(part, int) else (f".{part}" if out else str(part))
    return out or "<root>"
