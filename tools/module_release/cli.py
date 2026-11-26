"""Command-line entry point: ``python -m tools.module_release <command>``.

Commands:
  gate     SemVer interface-diff gate for one or more modules (exit 1 on failure)
  detect   JSON list of modules changed since a base commit
  info     version and tag of a module as key=value lines (for $GITHUB_OUTPUT)
  notes    release notes: the CHANGELOG section, or a generated diff summary
  index    register the declared version in the catalog (IACP_API_URL, IACP_INTERNAL_TOKEN)
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from pathlib import Path
from typing import Mapping, Sequence

from .catalog import CatalogClient, CatalogError, build_index_payload
from .gate import ReleaseGate
from .notes import changelog_section, release_notes
from .sources import GitError, GitRepository, ModuleWorkspace, modules_in_paths
from .versioning import GateResult, git_tag

ZERO_SHA = "0" * 40
COMMIT_SHA = re.compile(r"^[0-9a-f]{40}$")
API_URL_ENV = "IACP_API_URL"
TOKEN_ENV = "IACP_INTERNAL_TOKEN"


class App:
    """Wires the adapters together for one invocation."""

    def __init__(self, repo_root: Path) -> None:
        self.git = GitRepository(repo_root)
        self.workspace = ModuleWorkspace(repo_root)
        self.gate = ReleaseGate(self.workspace, self.git)

    def resolve_modules(self, names: Sequence[str], all_modules: bool) -> list[str]:
        known = self.workspace.module_names()
        if all_modules:
            return known
        unknown = sorted(set(names) - set(known))
        if unknown:
            raise ValueError(f"unknown module(s): {', '.join(unknown)} (known: {', '.join(known)})")
        if not names:
            raise ValueError("name at least one module, or pass --all")
        return list(dict.fromkeys(names))

    def untagged(self, names: Sequence[str]) -> list[str]:
        """Modules whose declared version has no tag yet."""
        return [n for n in names if not self.git.tags(git_tag(n, self.gate.declared_version(n)))]

    def detect(self, base: str | None, head: str, include_untagged: bool) -> list[str]:
        names = self.workspace.module_names()
        if base and base != ZERO_SHA and self.git.has_ref(base):
            changed = set(modules_in_paths(self.git.changed_paths(base, head))) & set(names)
        else:
            changed = set(names)
        if include_untagged:
            changed |= set(self.untagged(names))
        return sorted(changed)


def render_result(result: GateResult) -> str:
    previous = f"{result.previous}" if result.previous else "none"
    status = "ok  " if result.ok else "FAIL"
    lines = [
        f"{status} {result.module} {result.declared} (previous: {previous}, "
        f"required: {result.required.label}, declared bump: {result.actual.label})"
    ]
    lines.extend(f"     {c.render()}" for c in result.changes)
    lines.extend(f"     error: {e}" for e in result.errors)
    lines.extend(f"     warning: {w}" for w in result.warnings)
    return "\n".join(lines)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="python -m tools.module_release", description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--repo", type=Path, default=Path("."), help="repository root (default: .)")
    sub = parser.add_subparsers(dest="command", required=True)

    gate = sub.add_parser("gate", help="check that metadata.version is a large enough bump")
    gate.add_argument("modules", nargs="*", help="module names (directories under modules/)")
    gate.add_argument("--all", action="store_true", help="check every module")
    gate.add_argument("--format", choices=("text", "json"), default="text")

    detect = sub.add_parser("detect", help="print a JSON list of modules changed since --base")
    detect.add_argument("--base", default=None, help="base commit; all modules when missing or unknown")
    detect.add_argument("--head", default="HEAD")
    detect.add_argument("--include-untagged", action="store_true",
                        help="also include modules whose declared version is not tagged yet")

    info = sub.add_parser("info", help="print version=, tag=, prerelease= and tag_exists= lines")
    info.add_argument("module")

    notes = sub.add_parser("notes", help="print release notes for the declared version")
    notes.add_argument("module")

    index = sub.add_parser("index", help=f"POST the declared version to ${API_URL_ENV}/v1/internal/catalog/index")
    index.add_argument("module")
    index.add_argument("--commit-sha", required=True, help="40-character commit SHA the release tag points at")
    index.add_argument("--released-by", default=None,
                       help="recorded as released_by (default: $GITHUB_ACTOR, else module-release)")
    index.add_argument("--skip-if-unconfigured", action="store_true",
                       help=f"exit 0 with a notice when neither {API_URL_ENV} nor {TOKEN_ENV} is set "
                            "(setting only one of them is still an error)")
    index.add_argument("--dry-run", action="store_true", help="print the payload instead of sending it")
    return parser


def _gate(app: App, args: argparse.Namespace) -> int:
    results = [app.gate.check(m) for m in app.resolve_modules(args.modules, args.all)]
    if args.format == "json":
        print(json.dumps([r.to_dict() for r in results], indent=2))
    else:
        print("\n".join(render_result(r) for r in results))
        failed = sum(not r.ok for r in results)
        print(f"{len(results)} module(s) checked, {failed} failed")
    return 0 if all(r.ok for r in results) else 1


def _detect(app: App, args: argparse.Namespace) -> int:
    print(json.dumps(app.detect(args.base, args.head, args.include_untagged)))
    return 0


def _info(app: App, args: argparse.Namespace) -> int:
    (module,) = app.resolve_modules([args.module], False)
    version = app.gate.declared_version(module)
    tag = git_tag(module, version)
    print(f"module={module}")
    print(f"version={version}")
    print(f"tag={tag}")
    print(f"prerelease={'true' if version.is_prerelease else 'false'}")
    print(f"tag_exists={'true' if app.git.tags(tag) else 'false'}")
    return 0


def _notes(app: App, args: argparse.Namespace) -> int:
    (module,) = app.resolve_modules([args.module], False)
    print(release_notes(app.workspace.changelog(module), app.gate.check(module, before_declared=True)))
    return 0


def _index(app: App, args: argparse.Namespace, env: Mapping[str, str]) -> int:
    (module,) = app.resolve_modules([args.module], False)
    if not COMMIT_SHA.match(args.commit_sha):
        raise ValueError("--commit-sha must be a 40-character lowercase hex SHA")
    version = app.gate.declared_version(module)
    changelog = changelog_section(app.workspace.changelog(module), version)
    released_by = args.released_by or env.get("GITHUB_ACTOR") or "module-release"
    payload = build_index_payload(app.workspace.manifest(module), args.commit_sha, released_by, changelog)
    if args.dry_run:
        print(json.dumps(payload, indent=2))
        return 0
    url, token = env.get(API_URL_ENV, "").strip(), env.get(TOKEN_ENV, "").strip()
    if not url or not token:
        missing = " and ".join(n for n, v in ((API_URL_ENV, url), (TOKEN_ENV, token)) if not v)
        if args.skip_if_unconfigured and not url and not token:
            print(f"::notice title=Catalog index skipped::{missing} not configured, "
                  f"so {payload['git_tag']} was not indexed (local-first, ADR-0009)")
            return 0
        # Exactly one of the two set is a misconfiguration, never a reason to skip.
        raise ValueError(f"{missing} must be set to index {payload['git_tag']}")
    try:
        result = CatalogClient(url, token).index(payload)
    except CatalogError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print(f"{payload['git_tag']}: {'indexed' if result.created else 'already indexed'} (HTTP {result.status})")
    return 0


COMMANDS = {"gate": _gate, "detect": _detect, "info": _info, "notes": _notes}


def main(argv: Sequence[str] | None = None, env: Mapping[str, str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "index":
            return _index(App(args.repo), args, os.environ if env is None else env)
        return COMMANDS[args.command](App(args.repo), args)
    except (FileNotFoundError, ValueError, GitError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
