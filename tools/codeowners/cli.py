"""Command-line entry point: ``python -m tools.codeowners [--check]``.

Exit codes: 0 up to date (or written), 1 drift or an undeclared team, 2 bad config or I/O.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Sequence, TextIO

from .adapters import RepoFiles
from .models import ConfigError
from .render import drift, render, undeclared_teams

DEFAULT_CONFIG = ".github/teams.yaml"
DEFAULT_OUTPUT = ".github/CODEOWNERS"


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="python -m tools.codeowners", description=__doc__)
    p.add_argument("--repo-root", type=Path, default=Path("."), help="repository root (default: .)")
    p.add_argument("--config", default=DEFAULT_CONFIG, help=f"teams file (default: {DEFAULT_CONFIG})")
    p.add_argument("--output", default=DEFAULT_OUTPUT, help=f"CODEOWNERS path (default: {DEFAULT_OUTPUT})")
    p.add_argument("--check", action="store_true", help="fail when the committed file differs; write nothing")
    return p


def main(argv: Sequence[str] | None = None, out: TextIO = sys.stdout, err: TextIO = sys.stderr) -> int:
    args = build_parser().parse_args(argv)
    files = RepoFiles(args.repo_root)
    try:
        cfg = files.load_config(args.config)
        missing = undeclared_teams(cfg, files.stack_teams())
        expected = render(cfg)
        actual = files.read(args.output)
    except (ConfigError, OSError) as exc:
        print(f"codeowners: {exc}", file=err)
        return 2
    if missing:
        print(f"codeowners: stacks exist for team(s) without an entry in {args.config}: {', '.join(missing)}", file=err)
        return 1
    diff = drift(expected, actual, args.output)
    if args.check:
        if diff:
            err.writelines(diff)
            print(f"codeowners: {args.output} is out of date; run `python -m tools.codeowners`", file=err)
            return 1
        print(f"codeowners: {args.output} is up to date", file=out)
        return 0
    if diff:
        files.write(args.output, expected)
        print(f"codeowners: wrote {args.output}", file=out)
    else:
        print(f"codeowners: {args.output} already up to date", file=out)
    return 0
