"""Command-line entry point: ``python -m tools.stack_pipeline <command>``."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Sequence

from .adapters import FileSystemStackRepository, GitDiff
from .detection import DEFAULT_MAX_STACKS, DetectionError, DetectionPolicy, DetectionResult, StackDetector
from .models import StackMetadataError


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
    return parser


def _changed_files(args: argparse.Namespace) -> list[str]:
    if args.files_from is not None:
        text = sys.stdin.read() if str(args.files_from) == "-" else args.files_from.read_text(encoding="utf-8")
        return [line.strip() for line in text.splitlines() if line.strip()]
    return GitDiff(args.repo_root).changed_files(args.base, args.head)


def write_github_output(path: Path, result: DetectionResult) -> None:
    with path.open("a", encoding="utf-8") as fh:
        fh.write(f"matrix={json.dumps(result.matrix(), separators=(',', ':'))}\n")
        fh.write(f"count={result.count}\n")
        fh.write(f"full_run={str(result.full_run).lower()}\n")


def run_detect(args: argparse.Namespace) -> int:
    policy = DetectionPolicy(max_stacks=args.max_stacks, full_run_prefixes=tuple(args.full_run_prefix))
    result = StackDetector(FileSystemStackRepository(args.repo_root), policy).detect(_changed_files(args))
    print(json.dumps(result.to_dict(), indent=2))
    if args.github_output:
        write_github_output(Path(args.github_output), result)
    return 0


COMMANDS = {"detect": run_detect}


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return COMMANDS[args.command](args)
    except (DetectionError, StackMetadataError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
