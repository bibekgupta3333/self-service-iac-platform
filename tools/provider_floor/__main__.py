"""``python -m tools.provider_floor stage <module_dir> <dest>``: copy a module and pin its provider floors."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Sequence

from tools.module_release.interface import InterfaceError

from .floor import FloorError, ModuleStager


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="tools.provider_floor", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    stage = sub.add_parser("stage", help="copy the module to DEST with an override file pinning each provider floor")
    stage.add_argument("module_dir", type=Path)
    stage.add_argument("dest", type=Path, help="an empty or new directory")
    args = parser.parse_args(argv)
    try:
        pins = ModuleStager().stage(args.module_dir, args.dest)
    except (FloorError, InterfaceError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    for pin in pins:
        print(f"{pin.source}={pin.version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
