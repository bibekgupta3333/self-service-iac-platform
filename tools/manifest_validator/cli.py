"""Command-line entry point: ``python -m tools.manifest_validator modules/``."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Sequence

from .validator import ManifestValidator


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="python -m tools.manifest_validator",
        description="Validate modules/*/module.yaml against the schema and check parity with variables.tf/outputs.tf.",
    )
    parser.add_argument("modules_root", type=Path, help="path to the modules/ directory")
    parser.add_argument("--schema", type=Path, default=None, help="schema path (default: <modules_root>/_shared/module.schema.json)")
    parser.add_argument("--format", choices=("text", "json"), default="text", help="output format")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        report = ManifestValidator.for_root(args.modules_root, args.schema).run()
    except (FileNotFoundError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(report.render_json() if args.format == "json" else report.render_text())
    return 0 if report.ok else 1
