"""``python -m tools.required_checks --check``: fail when the ruleset names a job that does not exist."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Sequence

from .checks import RepositoryChecks


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="tools.required_checks", description=__doc__)
    parser.add_argument("--check", action="store_true", help="exit 1 when a required check is not a job name")
    parser.add_argument("--repo-root", type=Path, default=Path("."))
    args = parser.parse_args(argv)
    try:
        checks = RepositoryChecks(args.repo_root)
        found = checks.problems()
        contexts = checks.contexts()
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    for line in found:
        print(line, file=sys.stderr)
    if not found:
        print(f"{len(contexts)} required check(s) match a job name")
    return 1 if found and args.check else 0
