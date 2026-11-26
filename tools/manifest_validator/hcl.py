"""Pure helpers for reading the small subset of HCL the validator needs.

These functions do not parse HCL in general. They find top-level
``variable``, ``output`` and ``locals`` blocks by brace matching and pull
simple attributes out of them. That is enough for module contract checks
and avoids a third-party HCL parser dependency. All functions are pure:
callers (see loader.py) read the files.
"""

from __future__ import annotations

import re
from typing import Mapping

_BLOCK_HEADER = re.compile(r'^\s*(variable|output)\s+"([^"]+)"\s*\{', re.MULTILINE)
_LOCALS_HEADER = re.compile(r"^\s*locals\s*\{", re.MULTILINE)
_TYPE_ATTR = re.compile(r"^\s*type\s*=\s*([A-Za-z_]+)", re.MULTILINE)
_STRING_ATTR = r'^\s*{name}\s*=\s*"([^"]*)"'

_TYPE_FAMILIES: Mapping[str, str] = {
    "string": "string",
    "number": "number",
    "bool": "bool",
    "map": "map",
    "object": "map",
    "list": "list",
    "set": "list",
    "tuple": "list",
    "any": "any",
}


def strip_comments(text: str) -> str:
    """Remove ``#``, ``//`` and ``/* */`` comments, leaving string literals intact."""
    out: list[str] = []
    i, n = 0, len(text)
    in_string = False
    while i < n:
        ch = text[i]
        if in_string:
            out.append(ch)
            if ch == "\\" and i + 1 < n:
                out.append(text[i + 1])
                i += 2
                continue
            if ch == '"':
                in_string = False
            i += 1
            continue
        if ch == '"':
            in_string = True
            out.append(ch)
            i += 1
        elif ch == "#" or text.startswith("//", i):
            while i < n and text[i] != "\n":
                i += 1
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            i = n if end == -1 else end + 2
        else:
            out.append(ch)
            i += 1
    return "".join(out)


def block_body(text: str, open_brace: int) -> str:
    """Return the text between the brace at ``open_brace`` and its matching close."""
    depth = 0
    in_string = False
    escaped = False
    for idx in range(open_brace, len(text)):
        ch = text[idx]
        if in_string:
            if escaped:
                escaped = False
            elif ch == "\\":
                escaped = True
            elif ch == '"':
                in_string = False
            continue
        if ch == '"':
            in_string = True
        elif text[idx] == "{":
            depth += 1
        elif text[idx] == "}":
            depth -= 1
            if depth == 0:
                return text[open_brace + 1 : idx]
    raise ValueError(f"unbalanced braces from offset {open_brace}")


def find_blocks(text: str, kind: str) -> dict[str, str]:
    """Map block label to body for every top-level ``kind`` block (variable/output)."""
    clean = strip_comments(text)
    return {
        m.group(2): block_body(clean, m.end() - 1)
        for m in _BLOCK_HEADER.finditer(clean)
        if m.group(1) == kind
    }


def find_locals(text: str) -> str:
    """Concatenate the bodies of every ``locals`` block."""
    clean = strip_comments(text)
    return "\n".join(block_body(clean, m.end() - 1) for m in _LOCALS_HEADER.finditer(clean))


def string_attribute(body: str, name: str) -> str | None:
    """Return a literal string attribute (``name = "value"``) from a block body."""
    match = re.search(_STRING_ATTR.format(name=re.escape(name)), body, re.MULTILINE)
    return match.group(1) if match else None


def type_family(body: str) -> str | None:
    """Return the Terraform type family of a variable body, or None if untyped."""
    match = _TYPE_ATTR.search(body)
    if not match:
        return None
    return _TYPE_FAMILIES.get(match.group(1), "unknown")
