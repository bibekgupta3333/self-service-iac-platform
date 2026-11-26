"""Command-line entry point: ``python -m tools.stack_pipeline <command>``."""

from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Any, Sequence

from .adapters import FileSystemStackRepository, GitCli, GitDiff
from .detection import DEFAULT_MAX_STACKS, DetectionError, DetectionPolicy, DetectionResult, StackDetector
from .models import StackMetadataError
from .module_install import MANIFEST, ModuleInstallVerifier, parse_manifest
from .module_pins import DEFAULT_REPO_URL, DEFAULT_TRUSTED_REF, ModulePinVerifier, PinError, Resolution, insteadof_config
from .plan_summary import render_markdown, summarize
from .policy_context import build_context


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="python -m tools.stack_pipeline", description=__doc__)
    parser.add_argument("--repo-root", type=Path, default=Path("."), help="repository root (default: .)")
    sub = parser.add_subparsers(dest="command", required=True)

    detect = sub.add_parser("detect", help="map changed files to stack roots and emit a JSON matrix")
    source = detect.add_mutually_exclusive_group(required=True)
    source.add_argument("--base", help="base commit; diffs <base>...<head>")
    source.add_argument("--files-from", type=Path, help="file with one changed path per line ('-' for stdin)")
    detect.add_argument("--head", default="HEAD", help="head commit (default: HEAD)")
    detect.add_argument("--max-stacks", type=int, default=DEFAULT_MAX_STACKS, help="fail above this many stacks")
    detect.add_argument(
        "--full-run-prefix",
        action="append",
        default=[],
        help="path prefix whose change re-validates every stack (repeatable)",
    )
    detect.add_argument(
        "--github-output",
        type=str,
        default=os.environ.get("GITHUB_OUTPUT"),
        help="append matrix=, count= and full_run= here (default: $GITHUB_OUTPUT)",
    )

    context = sub.add_parser("context", help="write the conftest --data context for a stack")
    context.add_argument("stack_dir", type=Path)
    context.add_argument("--allowed-region", action="append", default=[], help="override the policy default (repeatable)")

    summary = sub.add_parser("summary", help="summarise a plan and policy result as Markdown and JSON")
    summary.add_argument("stack_dir", type=Path)
    summary.add_argument("--plan", type=Path, help="terraform show -json output (may be missing)")
    summary.add_argument("--conftest", type=Path, help="conftest --output json result (may be missing)")
    summary.add_argument("--plan-text", type=Path, help="terraform plan text output (may be missing)")
    summary.add_argument("--step", action="append", default=[], metavar="NAME=OUTCOME", help="step outcome (repeatable)")
    summary.add_argument("--commit", default="", help="head commit shown in the comment")
    summary.add_argument("--run-url", default="", help="workflow run URL shown in the comment")
    summary.add_argument("--markdown-out", type=Path, required=True)
    summary.add_argument("--json-out", type=Path, required=True)

    pins = sub.add_parser("verify-pins", help="verify module pins; serve unpublished tags from a local mirror")
    pins.add_argument("stack_dirs", type=Path, nargs="+")
    pins.add_argument("--mirror", type=Path, required=True, help="bare mirror for unpublished tags (created if needed)")
    pins.add_argument("--trusted-ref", default=DEFAULT_TRUSTED_REF, help="unpublished pins must be on this ref")
    pins.add_argument("--remote", default="origin", help="remote that holds published tags")
    pins.add_argument("--repo-url", default=DEFAULT_REPO_URL, help="module repository URL used in stack sources")
    pins.add_argument(
        "--github-env",
        type=str,
        default=os.environ.get("GITHUB_ENV"),
        help="append GIT_CONFIG_* here; otherwise print export lines (default: $GITHUB_ENV)",
    )

    installed = sub.add_parser("verify-modules", help="after init: match installed modules to the pins one to one")
    installed.add_argument("stack_dir", type=Path)
    installed.add_argument("--repo-url", default=DEFAULT_REPO_URL, help="module repository URL used in stack sources")
    return parser


# ---- small I/O helpers (the CLI is the adapter layer) ------------------------------


def _changed_files(args: argparse.Namespace) -> list[str]:
    if args.files_from is not None:
        text = sys.stdin.read() if str(args.files_from) == "-" else args.files_from.read_text(encoding="utf-8")
        return [line.strip() for line in text.splitlines() if line.strip()]
    return GitDiff(args.repo_root).changed_files(args.base, args.head)


def _read_json(path: Path | None) -> Any | None:
    """Parsed JSON, or None when the file is absent, empty or not JSON (the step failed)."""
    if path is None or not path.is_file():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return None


def _read_text(path: Path | None) -> str | None:
    return path.read_text(encoding="utf-8", errors="replace") if path is not None and path.is_file() else None


def parse_steps(pairs: Sequence[str]) -> dict[str, str]:
    """``["fmt=success", "plan=failure"]`` -> ordered mapping."""
    steps: dict[str, str] = {}
    for pair in pairs:
        name, sep, outcome = pair.partition("=")
        if not sep or not name:
            raise ValueError(f"--step expects NAME=OUTCOME, got {pair!r}")
        steps[name] = outcome or "skipped"
    return steps


def write_github_output(path: Path, result: DetectionResult) -> None:
    with path.open("a", encoding="utf-8") as fh:
        fh.write(f"matrix={json.dumps(result.matrix(), separators=(',', ':'))}\n")
        fh.write(f"count={result.count}\n")
        fh.write(f"full_run={str(result.full_run).lower()}\n")


# ---- commands --------------------------------------------------------------------


def run_detect(args: argparse.Namespace) -> int:
    policy = DetectionPolicy(max_stacks=args.max_stacks, full_run_prefixes=tuple(args.full_run_prefix))
    result = StackDetector(FileSystemStackRepository(args.repo_root), policy).detect(_changed_files(args))
    print(json.dumps(result.to_dict(), indent=2))
    if args.github_output:
        write_github_output(Path(args.github_output), result)
    return 0


def run_context(args: argparse.Namespace) -> int:
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    print(json.dumps(build_context(meta, args.allowed_region), indent=2))
    return 0


def run_summary(args: argparse.Namespace) -> int:
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    conftest_output = _read_json(args.conftest)
    result = summarize(
        stack_id=meta.ref.id,
        stack_path=meta.ref.path,
        plan=_read_json(args.plan),
        conftest_output=conftest_output if isinstance(conftest_output, list) else None,
        steps=parse_steps(args.step),
    )
    args.markdown_out.write_text(
        render_markdown(result, _read_text(args.plan_text), commit=args.commit, run_url=args.run_url), encoding="utf-8"
    )
    args.json_out.write_text(json.dumps(result.to_dict(), indent=2) + "\n", encoding="utf-8")
    print(f"{meta.ref.path}: {'ok' if result.ok else 'failed'} {result.counts}")
    return 0


def run_verify_pins(args: argparse.Namespace) -> int:
    repo = FileSystemStackRepository(args.repo_root)
    stacks = [repo.load_dir(d) for d in args.stack_dirs]
    verifier = ModulePinVerifier(GitCli(args.repo_root, args.remote), args.mirror, args.trusted_ref)
    checks = verifier.verify(stacks)
    for check in checks:
        print(check.render(), file=sys.stderr)
    if any(c.resolution is Resolution.BOOTSTRAPPED for c in checks):
        env = insteadof_config(args.mirror, args.repo_url)
        if args.github_env:
            with Path(args.github_env).open("a", encoding="utf-8") as fh:
                fh.writelines(f"{k}={v}\n" for k, v in env.items())
        else:
            print("\n".join(f"export {k}={shlex.quote(v)}" for k, v in env.items()))
    return 0


def run_verify_modules(args: argparse.Namespace) -> int:
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    manifest = Path(args.stack_dir) / MANIFEST
    data = _read_json(manifest)
    if data is None:
        raise PinError(f"{manifest} is missing or unreadable; run terraform init first")
    verifier = ModuleInstallVerifier(GitCli(args.repo_root), args.repo_url)
    for line in verifier.verify(Path(args.stack_dir), meta, parse_manifest(data)):
        print(line)
    return 0


COMMANDS = {"detect": run_detect, "context": run_context, "summary": run_summary, "verify-pins": run_verify_pins, "verify-modules": run_verify_modules}


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return COMMANDS[args.command](args)
    except (DetectionError, StackMetadataError, PinError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
