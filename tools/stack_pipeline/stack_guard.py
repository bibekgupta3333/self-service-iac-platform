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
  scanner does not read;
- the *entries* of the root: only the files the generator writes plus the lock file. Any
  directory (``.terraform``, ``.terraform.d``: a committed provider binary there is reused by
  ``terraform init`` without any download or signature check), symlink, dotfile or other file
  is refused;
- the lock file's *pins*: every provider version and every hash in it must already be in the
  lock files of the base commit (``lock_allowlist``), so a provider package the PR made up,
  with a matching ``h1:`` hash, is refused.

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
# What a stack root may contain: the files backend/internal/generator writes (render.go) and the lock.
ALLOWED_FILES = frozenset({"main.tf", "versions.tf", "providers.tf", "backend.tf", "outputs.tf", "variables.tf",
                           "iacp.stack.json", LOCK_FILE})
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
    # Terraform decodes HCL string escapes and templates, this scanner does not: "h1:...\u003d" or
    # an interpolation would be a different hash to Terraform than to the pin check. A lock file
    # written by `terraform init` has neither, so refuse them outright.
    for marker, what in (("\\", "a backslash escape"), ("${", "a ${ template"), ("%{", "a %{ template"),
                         ("<<", "a heredoc")):
        if marker in text:
            line = text[:text.index(marker)].count("\n") + 1
            found.append(Violation(path, line, "lock", f"the lock file contains {what}, which Terraform decodes but the pin "
                                   "check does not; `terraform init` never writes one"))
    try:
        lock_entries(text)
    except HclScanError as exc:
        found.append(Violation(path, 1, "unreadable", f"the lock file cannot be read the way the pin check reads it ({exc})"))
    for b in blocks:
        if b.depth != 0:
            continue
        address = _normalise_source(b.labels[0]) if b.kind == "provider" and b.labels else ""
        if address not in allowed:
            found.append(Violation(path, b.line, "provider",
                                   f"lock file entry {b.kind} {' '.join(b.labels)!r} is not an allow-listed provider "
                                   f"({', '.join(sorted(allowed))})"))
    return found


def strip_comments(text: str) -> str:
    """``text`` with ``#``, ``//`` and ``/* */`` comments removed outside strings (newlines kept,
    so line numbers stay). A commented-out ``hashes = [...]`` or a ``]`` in a comment must not
    steer what the pin check reads. Heredocs are not understood: refuse them first. Pure."""
    out: list[str] = []
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if c == '"':
            j = _skip_string(text, i)
            out.append(text[i:j])
            i = j
        elif c == "#" or text.startswith("//", i):
            while i < n and text[i] != "\n":
                i += 1
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            if end < 0:
                raise HclScanError("unterminated comment")
            out.append("\n" * text.count("\n", i, end))
            i = end + 2
        else:
            out.append(c)
            i += 1
    return "".join(out)


def _attribute_value_start(body: str, name: str) -> int:
    """Index just after ``name =`` of the one attribute called ``name`` in a comment-free provider
    block body. Zero or several of them is an error: Terraform rejects a duplicate attribute, and a
    second one must never hide behind the first. Pure."""
    found = [m.end() for m in re.finditer(rf"(?m)^[ \t]*{name}[ \t]*=[ \t]*", body)]
    if len(found) != 1:
        raise HclScanError(f"the provider block has {len(found)} {name!r} attributes, want exactly one")
    return found[0]


def _quoted_strings_in_list(body: str, start: int) -> list[str]:
    """The string literals of the ``[ ... ]`` list that starts at ``start``, read with the string
    scanner so a ``]`` or a comma inside a string cannot end it early. Pure."""
    if start >= len(body) or body[start] != "[":
        raise HclScanError("hashes is not a list")
    out: list[str] = []
    i, n = start + 1, len(body)
    while i < n:
        c = body[i]
        if c == '"':
            j = _skip_string(body, i)
            out.append(body[i + 1:j - 1])
            i = j
        elif c == "]":
            return out
        elif c in "[{(":
            raise HclScanError("the hashes list holds something other than strings")
        else:
            i += 1
    raise HclScanError("unterminated hashes list")

PinTable = Mapping[tuple[str, str], frozenset[str]]


def lock_entries(text: str) -> list[tuple[str, str, frozenset[str]]]:
    """``(provider address, version, hashes)`` of every provider block in a lock file. Pure."""
    out = []
    for b in parse_blocks(text):
        if b.depth == 0 and b.kind == "provider" and b.labels:
            body = strip_comments(b.body)
            v = _attribute_value_start(body, "version")
            if body[v] != '"':
                raise HclScanError("version is not a string")
            version = body[v + 1:_skip_string(body, v) - 1]
            try:
                hashes_at = _attribute_value_start(body, "hashes")
            except HclScanError as exc:
                if "0 'hashes'" not in str(exc):
                    raise
                hashes = frozenset()
            else:
                # Every quoted string in the list counts, valid hash or not: one that is not a plain
                # hash is then simply not in the allow-list, instead of vanishing from the comparison.
                hashes = frozenset(_quoted_strings_in_list(body, hashes_at))
            out.append((_normalise_source(b.labels[0]), version, hashes))
    return out


def lock_allowlist(texts: Iterable[str]) -> PinTable:
    """The pins the base commit already trusts: ``{(address, version): hashes}``, the union over
    the given lock files (the base commit's stack locks). Unreadable files contribute nothing. Pure."""
    table: dict[tuple[str, str], frozenset[str]] = {}
    for text in texts:
        try:
            entries = lock_entries(text)
        except HclScanError:
            continue
        for address, version, hashes in entries:
            table[(address, version)] = table.get((address, version), frozenset()) | hashes
    return table


def check_lock_pins(path: str, text: str, allowed: PinTable) -> list[Violation]:
    """Violations for a lock file whose provider versions or hashes are not in ``allowed``. Pure.

    A provider package the PR committed under ``.terraform`` is only accepted by ``terraform init``
    when the lock lists its hash; requiring every hash to be one the base commit already knows
    (the registry's, recorded by an earlier reviewed lock) closes that route even for a lock file.
    """
    try:
        entries = lock_entries(text)
    except HclScanError:
        return []  # reported as unreadable by check_lock
    found: list[Violation] = []
    for address, version, hashes in entries:
        known = allowed.get((address, version))
        if known is None:
            found.append(Violation(path, 1, "lock", f"provider {address} {version or '(no version)'} is not pinned by any "
                                   "lock file on the base commit; a new provider version needs a platform change first"))
        elif not hashes:
            found.append(Violation(path, 1, "lock", f"provider {address} {version} lists no hashes"))
        elif not hashes <= known:
            extra = sorted(hashes - known)
            found.append(Violation(path, 1, "lock", f"provider {address} {version} carries {len(extra)} hash(es) the base "
                                   f"commit does not know ({extra[0]}...); a provider package the PR made up cannot be pinned"))
    return found


def check_entries(entries: Mapping[str, str]) -> list[Violation]:
    """Violations for the entries of a stack root, given ``{name: kind}`` with kind ``file``, ``dir``,
    ``symlink`` or ``other``. Only ALLOWED_FILES as regular files pass. Pure."""
    found: list[Violation] = []
    for name in sorted(entries):
        kind = entries[name]
        if kind == "file" and name in ALLOWED_FILES:
            continue
        what = {"dir": "directories", "symlink": "symbolic links", "other": "special files"}.get(kind, "other files")
        found.append(Violation(name, 1, "entry", f"{what} such as {name!r} are not allowed in a stack root: it holds only "
                               f"{', '.join(sorted(ALLOWED_FILES))}. A directory like .terraform can hold provider "
                               "binaries that terraform init reuses without a download"))
    return found


def check_stack(files: Mapping[str, str], repo_url: str = DEFAULT_REPO_URL,
                providers: Iterable[str] = ALLOWED_PROVIDERS, pins: PinTable | None = None) -> list[Violation]:
    """Violations in a stack root, given ``{file name: text}`` for its top-level files. Pure.

    ``*.tf`` files and the lock file are scanned; a ``*.tf.json`` file is refused because
    Terraform would load it and this scanner does not read JSON configuration. With ``pins``
    (the base commit's lock allow-list) the lock file's versions and hashes are checked too.
    """
    found: list[Violation] = []
    for name in sorted(files):
        if name.endswith(".tf.json"):
            found.append(Violation(name, 1, "json", "JSON configuration is not allowed in a stack root"))
        elif name.endswith(".tf"):
            found += check_tf(name, files[name], repo_url, providers)
        elif name == LOCK_FILE:
            found += check_lock(name, files[name], providers)
            if pins is not None:
                found += check_lock_pins(name, files[name], pins)
    return found
