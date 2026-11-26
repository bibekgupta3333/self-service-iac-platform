"""Command-line entry point: ``python -m tools.ttl_reaper``."""

from __future__ import annotations

import argparse
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Mapping, Sequence

from tools.stack_pipeline.api_client import ApiError, FreshCredentials, TokenSource
from tools.stack_pipeline.report import ReportError

from .models import DEFAULT_MAX_TEARDOWNS, DueListError, escape, needs_attention, render_summary
from .reaper import Reaper, TtlApiClient

API_URL_ENV = "IACP_API_URL"
AUDIENCE_ENV = "IACP_OIDC_AUDIENCE"


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="python -m tools.ttl_reaper", description=__doc__)
    parser.add_argument("--api-url", default=os.environ.get(API_URL_ENV, ""), help=f"default: ${API_URL_ENV}")
    parser.add_argument("--audience", default=os.environ.get(AUDIENCE_ENV, ""),
                        help=f"GitHub OIDC audience (default: ${AUDIENCE_ENV}); without OIDC, $IACP_INTERNAL_TOKEN is used")
    parser.add_argument("--skip-if-unconfigured", action="store_true",
                        help=f"exit 0 with a ::notice:: when ${API_URL_ENV} is empty (the local-first default)")
    parser.add_argument("--max-teardowns", type=int, default=DEFAULT_MAX_TEARDOWNS,
                        help="destroy requests to open per run; the rest wait for the next run (default: %(default)s)")
    parser.add_argument("--dry-run", action="store_true", help="list what would be done and change nothing")
    parser.add_argument("--summary-file", type=Path, default=os.environ.get("GITHUB_STEP_SUMMARY") or None,
                        help="append a Markdown summary here (default: $GITHUB_STEP_SUMMARY)")
    return parser


def _client(args: argparse.Namespace, env: Mapping[str, str]) -> TtlApiClient:
    # One OIDC token per request: the API rejects a reused jti, so each call mints a new one.
    credential = FreshCredentials.start(TokenSource(env, stdout=sys.stdout), args.audience)
    if credential is None:
        raise ApiError(f"no credential for {args.api_url}: grant the job id-token: write and set {AUDIENCE_ENV}, "
                       "or provide IACP_INTERNAL_TOKEN")
    return TtlApiClient(args.api_url, credential)


def run(args: argparse.Namespace, env: Mapping[str, str]) -> int:
    if not args.api_url.strip():
        if not args.skip_if_unconfigured:
            raise ValueError(f"{API_URL_ENV} is not set; pass --api-url or --skip-if-unconfigured")
        print(f"::notice title=Platform API not configured::TTL reaper skipped: {API_URL_ENV} is empty (local-first, ADR-0009)")
        return 0
    now = datetime.now(timezone.utc)
    result = Reaper(_client(args, env), max_teardowns=args.max_teardowns, dry_run=args.dry_run).run()
    for o in result.outcomes:
        line = f"{o.action.kind} {o.action.stack.path}: {o.status}: {o.detail}"
        if o.failed:
            print(f"::error title=TTL {o.action.kind} failed::{escape(line)}")
        elif needs_attention(o, now):
            print(f"::warning title=TTL {o.status}::{escape(line)}")
        else:
            print(line)
    if result.deferred:
        print(f"::warning title=TTL teardown cap reached::{len(result.deferred)} expired stack(s) wait for the next run")
    if not result.outcomes:
        print("nothing is due")
    if args.summary_file:
        with Path(args.summary_file).open("a", encoding="utf-8") as fh:
            fh.write(render_summary(result))
    return 0 if result.ok else 1


def main(argv: Sequence[str] | None = None, env: Mapping[str, str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return run(args, os.environ if env is None else env)
    except (ApiError, DueListError, ReportError, ValueError) as exc:
        print(f"::error title=TTL reaper::{escape(str(exc))}")
        return 1
