"""Static check of a stack root's HCL before ``terraform init`` and ``terraform plan``.

``terraform plan`` runs code: ``data "external"`` starts a program, ``data "http"`` calls
out, and a provider is a binary. A stack root is written by the pull request, so a plan
of an unchecked root lets the PR run code on the runner that holds the plan, the tools
and the policy the gates read. The OPA rules that forbid these constructs
(``iacp.forbidden.*``) only see them in the plan JSON, after the program has run.

Generated stacks (``backend/internal/generator``) contain nothing but ``terraform``,
``provider``, ``module``, ``variable``, ``output``, ``locals`` and ``moved`` blocks, so
this guard refuses everything else, which also covers every ``resource``, ``data`` and
provisioner. It then checks what may be *loaded*:

- providers: only those in ``ALLOWED_PROVIDERS``, in ``required_providers``, in
  ``provider`` blocks and in ``.terraform.lock.hcl``;
- modules: only the platform repository at an exact release tag
  (``git::<repo>.git//modules/<name>?ref=modules/<name>/vX.Y.Z``), never a local path;
- backend: only ``local`` and ``s3``, the two the generator emits (``cloud``, ``http`` and the rest are refused);
- ``provisioner`` and ``connection`` blocks anywhere, and ``*.tf.json`` files, which this
  scanner does not read.

Everything here is pure: it takes file contents and returns violations. Reading the
directory (and refusing symlinks) is the adapter's job (``cli.run_check_stack``).
It is meant to run from the *base* commit's tools, so a PR cannot weaken it.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Iterable, Mapping

DEFAULT_REPO_URL = "https://github.com/bibekgupta3333/self-service-iac-platform.git"
ALLOWED_PROVIDERS = frozenset({"hashicorp/aws", "hashicorp/random"})
ALLOWED_TOP_LEVEL = frozenset({"terraform", "provider", "module", "variable", "output", "locals", "moved"})
ALLOWED_BACKENDS = frozenset({"local", "s3"})  # what the generator emits (s3 after local-first, ADR-0009)
FORBIDDEN_ANYWHERE = frozenset({"provisioner", "connection"})
LOCK_FILE = ".terraform.lock.hcl"
_REGISTRY = "registry.terraform.io/"


class HclScanError(ValueError):
    """The text cannot be scanned (unterminated string, comment or block)."""


@dataclass(frozen=True)
class Block:
    """One HCL block: ``kind "label" ... { body }``."""

    kind: str
    labels: tuple[str, ...]
    depth: int  # 0 = top level
    parent: str | None
    body: str  # text between the braces
    line: int


@dataclass(frozen=True)
class Violation:
    path: str
    line: int
    rule: str
    message: str

    def render(self) -> str:
        return f"{self.path}:{self.line}: [{self.rule}] {self.message}"


_IDENT = re.compile(r"[A-Za-z_][A-Za-z0-9_-]*")
_HEREDOC = re.compile(r"<<(-?)([A-Za-z_][A-Za-z0-9_]*)[ \t]*\r?\n")


def _skip_string(text: str, i: int) -> int:
    """Index just past the quoted string that starts at ``i`` (a ``"``), honouring escapes
    and nested ``${ ... }`` / ``%{ ... }`` templates, which may hold strings of their own."""
    n = len(text)
    i += 1
    while i < n:
        c = text[i]
        if c == "\\":
            i += 2
        elif c == '"':
            return i + 1
        elif c in "$%" and text.startswith("{", i + 1) and not text.startswith(c + c, i - 1 if i else 0):
            i = _skip_braces(text, i + 1)
        else:
            i += 1
    raise HclScanError("unterminated string")


def _skip_braces(text: str, i: int) -> int:
    """Index just past the ``{ ... }`` that starts at ``i``, skipping strings inside it."""
    depth = 0
    n = len(text)
    while i < n:
        c = text[i]
        if c == '"':
            i = _skip_string(text, i)
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return i + 1
        i += 1
    raise HclScanError("unterminated template expression")


def parse_blocks(text: str) -> list[Block]:
    """Every block in ``text`` (depth-first, in source order). Pure; raises ``HclScanError``.

    A block is a line header ``IDENT (IDENT | "string")* {``. ``name = {`` (an object
    value) and anything inside parentheses or brackets are not blocks.
    """
    blocks: list[Block] = []
    stack: list[tuple[str, int, int, tuple[str, ...], int]] = []  # kind, body start, depth, labels, line
    header: list[tuple[str, str]] = []  # (type, text) tokens of the current line
    paren = 0
    line = 1
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if c == "\n":
            line += 1
            if paren == 0:
                header = []
            i += 1
        elif c in " \t\r":
            i += 1
        elif c == "#" or text.startswith("//", i):
            while i < n and text[i] != "\n":
                i += 1
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            if end < 0:
                raise HclScanError("unterminated comment")
            line += text.count("\n", i, end)
            i = end + 2
        elif c == '"':
            j = _skip_string(text, i)
            line += text.count("\n", i, j)
            raw = text[i + 1:j - 1]
            header.append(("string", raw))
            i = j
        elif text.startswith("<<", i) and (m := _HEREDOC.match(text, i)):
            marker = m.group(2)
            body_start = m.end()
            end = re.compile(rf"^[ \t]*{re.escape(marker)}[ \t]*$", re.M).search(text, body_start)
            if end is None:
                raise HclScanError(f"unterminated heredoc <<{marker}")
            line += text.count("\n", i, end.end())
            header.append(("string", ""))
            i = end.end()
        elif c in "([":
            paren += 1
            header.append(("punct", c))
            i += 1
        elif c in ")]":
            paren = max(0, paren - 1)
            header.append(("punct", c))
            i += 1
        elif c == "{":
            is_block = (
                paren == 0
                and header
                and header[0][0] == "ident"
                and all(t in ("ident", "string") for t, _ in header)
            )
            if is_block:
                stack.append((header[0][1], i + 1, len(stack), tuple(s for _, s in header[1:]), line))
            else:
                stack.append(("", i + 1, len(stack), (), line))  # an object literal: tracked for balance only
            header = []
            i += 1
        elif c == "}":
            if not stack:
                raise HclScanError(f"unbalanced '}}' on line {line}")
            kind, start, depth, labels, opened = stack.pop()
            if kind:
                parent = next((k for k, *_ in reversed(stack) if k), None)
                blocks.append(Block(kind, labels, sum(1 for k, *_ in stack if k), parent, text[start:i], opened))
            header = []
            i += 1
        else:
            m = _IDENT.match(text, i)
            if m:
                header.append(("ident", m.group()))
                i = m.end()
            else:
                header.append(("punct", c))
                i += 1
    if stack:
        raise HclScanError(f"unterminated block opened on line {stack[-1][4]}")
    blocks.sort(key=lambda b: b.line)
    return blocks


_ATTR = re.compile(r'(?m)^[ \t]*source[ \t]*=[ \t]*"([^"\n]*)"')
_REQUIRED = re.compile(r'(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_-]*)[ \t]*=[ \t]*(\{[^{}]*\}|"[^"\n]*")')


def provider_sources(required_providers_body: str) -> dict[str, str]:
    """``{"aws": "hashicorp/aws"}`` for a ``required_providers`` body; a provider without a
    ``source`` gets the implied ``hashicorp/<name>``. Pure."""
    out: dict[str, str] = {}
    for name, value in _REQUIRED.findall(required_providers_body):
        m = _ATTR.search(value.strip("{}"))
        out[name] = _normalise_source(m.group(1)) if m else f"hashicorp/{name}"
    return out


def _normalise_source(source: str) -> str:
    return source[len(_REGISTRY):] if source.startswith(_REGISTRY) else source


def module_pattern(repo_url: str) -> re.Pattern[str]:
    return re.compile(
        rf"git::{re.escape(repo_url)}//modules/(?P<name>[a-z0-9-]+)\?ref=modules/(?P=name)/v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?"
    )


def check_tf(path: str, text: str, repo_url: str = DEFAULT_REPO_URL,
             providers: Iterable[str] = ALLOWED_PROVIDERS) -> list[Violation]:
    """Violations in one ``.tf`` file. Pure."""
    allowed = frozenset(providers)
    try:
        blocks = parse_blocks(text)
    except HclScanError as exc:
        return [Violation(path, 1, "unreadable", f"the file cannot be scanned ({exc}); fix the syntax")]
    found: list[Violation] = []
    module_re = module_pattern(repo_url)

    def add(block: Block, rule: str, message: str) -> None:
        found.append(Violation(path, block.line, rule, message))

    for b in blocks:
        label = " ".join(f'"{x}"' for x in b.labels)
        if b.depth == 0 and b.kind not in ALLOWED_TOP_LEVEL:
            add(b, "block", f"{b.kind} {label} is not allowed in a stack root: it can run code at plan time. "
                "Stacks contain module calls only; put resources in a platform module")
        if b.kind in FORBIDDEN_ANYWHERE:
            add(b, "provisioner", f"{b.kind} blocks run commands and are not allowed")
        if b.kind == "provider" and b.depth == 0 and (not b.labels or b.labels[0] not in {p.split("/")[-1] for p in allowed}):
            add(b, "provider", f"provider {label or '(unnamed)'} is not in the allow-list "
                f"({', '.join(sorted(allowed))})")
        if b.kind == "required_providers":
            for name, source in provider_sources(b.body).items():
                if source not in allowed:
                    add(b, "provider", f"required provider {name} has source {source!r}, which is not in the "
                        f"allow-list ({', '.join(sorted(allowed))})")
        if b.kind == "backend" and (not b.labels or b.labels[0] not in ALLOWED_BACKENDS):
            add(b, "backend", f"backend {label or '(unnamed)'} is not allowed; stacks use the local or s3 backend")
        if b.kind == "cloud" and b.parent == "terraform":
            add(b, "backend", "a cloud block is not allowed; stacks use the local or s3 backend")
        if b.kind == "module" and b.depth == 0:
            m = _ATTR.search(b.body)
            if not m or not module_re.fullmatch(m.group(1)):
                add(b, "module", f"module {label} must use source "
                    f"git::{repo_url}//modules/<name>?ref=modules/<name>/vX.Y.Z (got "
                    f"{m.group(1) if m else 'no source'!r}); local and third-party modules are not allowed")
    return found


def check_lock(path: str, text: str, providers: Iterable[str] = ALLOWED_PROVIDERS) -> list[Violation]:
    """Violations in a ``.terraform.lock.hcl``: only allow-listed provider addresses. Pure."""
    allowed = frozenset(providers)
    try:
        blocks = parse_blocks(text)
    except HclScanError as exc:
        return [Violation(path, 1, "unreadable", f"the lock file cannot be scanned ({exc})")]
    found: list[Violation] = []
    for b in blocks:
        if b.depth != 0:
            continue
        address = _normalise_source(b.labels[0]) if b.kind == "provider" and b.labels else ""
        if address not in allowed:
            found.append(Violation(path, b.line, "provider",
                                   f"lock file entry {b.kind} {' '.join(b.labels)!r} is not an allow-listed provider "
                                   f"({', '.join(sorted(allowed))})"))
    return found


def check_stack(files: Mapping[str, str], repo_url: str = DEFAULT_REPO_URL,
                providers: Iterable[str] = ALLOWED_PROVIDERS) -> list[Violation]:
    """Violations in a stack root, given ``{file name: text}`` for its top-level files. Pure.

    ``*.tf`` files and the lock file are scanned; a ``*.tf.json`` file is refused because
    Terraform would load it and this scanner does not read JSON configuration.
    """
    found: list[Violation] = []
    for name in sorted(files):
        if name.endswith(".tf.json"):
            found.append(Violation(name, 1, "json", "JSON configuration is not allowed in a stack root"))
        elif name.endswith(".tf"):
            found += check_tf(name, files[name], repo_url, providers)
        elif name == LOCK_FILE:
            found += check_lock(name, files[name], providers)
    return found
