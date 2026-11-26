"""Repository-relative SARIF for code scanning (WBS 6.1).

TFLint and Checkov run inside a stack root, so their SARIF names files relative
to the stack (``main.tf``). Checkov also scans the pinned modules it downloaded
to a temporary directory and names those files by absolute path. Code scanning
needs paths relative to the repository root, so ``rebase_sarif`` rewrites every
``artifactLocation.uri``:

- a stack-relative path is joined to the stack root;
- a file of a downloaded module (``<download dir>/.../modules/<name>/vX.Y.Z/<path>``)
  becomes ``<path>``, the module's file in this repository (at its pinned tag, so
  line numbers can differ from the default branch);
- anything else, such as a path that leaves the stack root, points at the stack's
  ``main.tf``, where the module calls are.

Everything here is pure.
"""

from __future__ import annotations

import copy
import posixpath
import re
from typing import Any, Iterable, Mapping

FALLBACK_FILE = "main.tf"
# The pinned tag is part of the download path; the repository path follows it.
_MODULE_FILE_RE = re.compile(r"(?:^|/)modules/[a-z0-9_-]+/v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]*)?/(.+)$")
_SCHEME_RE = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*://")


class SarifError(ValueError):
    """The input is not a SARIF 2.1 log."""


def _strip_scheme(uri: str) -> str:
    return uri[len("file://"):] if uri.startswith("file://") else uri


def _inside(path: str, directory: str) -> str | None:
    """``path`` relative to ``directory`` (both compared without a leading slash), or None."""
    path, directory = path.lstrip("/"), directory.strip("/")
    if directory and path.startswith(directory + "/"):
        return path[len(directory) + 1:]
    return None


def rebase_uri(uri: str, stack_path: str, external_dirs: Iterable[str] = (), repo_root: str = "") -> str:
    """One SARIF uri as a path relative to the repository root."""
    stack_path = stack_path.strip("/")
    fallback = f"{stack_path}/{FALLBACK_FILE}"
    path = _strip_scheme(uri.strip())
    if not path or _SCHEME_RE.match(path):
        return fallback
    for directory in external_dirs:
        rest = _inside(path, directory)
        if rest is not None:
            match = _MODULE_FILE_RE.search(rest)
            return posixpath.normpath(match.group(1)) if match else fallback
    if repo_root:
        rest = _inside(path, repo_root)
        if rest is not None:
            return _contained(posixpath.normpath(rest), fallback)
    if path.startswith("/"):
        return fallback
    joined = posixpath.normpath(posixpath.join(stack_path, path))
    return joined if joined.startswith(stack_path + "/") else fallback


def _contained(path: str, fallback: str) -> str:
    return fallback if path in (".", "..") or path.startswith("../") else path


def rebase_sarif(
    doc: Any, stack_path: str, external_dirs: Iterable[str] = (), repo_root: str = ""
) -> dict[str, Any]:
    """A copy of the SARIF log with every artifact location relative to the repository root."""
    if not isinstance(doc, Mapping) or not isinstance(doc.get("runs"), list):
        raise SarifError("not a SARIF log: expected an object with a runs array")
    dirs = tuple(external_dirs)
    out = copy.deepcopy(dict(doc))

    def fix(node: Any) -> None:
        if isinstance(node, dict):
            for key, value in node.items():
                if key == "artifactLocation" and isinstance(value, dict) and isinstance(value.get("uri"), str):
                    value["uri"] = rebase_uri(value["uri"], stack_path, dirs, repo_root)
                    value.pop("uriBaseId", None)
                elif key == "artifacts" and isinstance(value, list):
                    for artifact in value:
                        location = artifact.get("location") if isinstance(artifact, dict) else None
                        if isinstance(location, dict) and isinstance(location.get("uri"), str):
                            location["uri"] = rebase_uri(location["uri"], stack_path, dirs, repo_root)
                            location.pop("uriBaseId", None)
                else:
                    fix(value)
        elif isinstance(node, list):
            for item in node:
                fix(item)

    fix(out["runs"])
    return out


def result_count(doc: Mapping[str, Any]) -> int:
    """Number of results across all runs."""
    return sum(len(run.get("results") or []) for run in doc.get("runs", []) if isinstance(run, Mapping))
