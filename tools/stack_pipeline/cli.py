"""Command-line entry point: ``python -m tools.stack_pipeline <command>``."""

from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Any, Mapping, Sequence

from .adapters import AwsCliTagReader, FileSystemStackRepository, GitCli, GitDiff
from .api_client import ApiError, FreshCredentials, IacpApiClient, TokenSource
from .cost import parse_infracost
from .detection import DEFAULT_MAX_STACKS, DetectionError, DetectionPolicy, DetectionResult, StackDetector
from .models import StackMetadataError
from .module_install import MANIFEST, ModuleInstallVerifier, parse_manifest
from .module_pins import DEFAULT_REPO_URL, DEFAULT_TRUSTED_REF, ModulePinVerifier, PinError, Resolution, insteadof_config
from .plan_summary import render_markdown, summarize
from .policy_context import build_context
from .sarif import SarifError, rebase_sarif, result_count
from .smoke import SmokeTester
from .report import (
    GitHubRun,
    ReportError,
    RunReport,
    error_block,
    plan_block,
    policy_block,
    redact_outputs,
    stage_status,
    summary_status,
)

API_URL_ENV = "IACP_API_URL"
AUDIENCE_ENV = "IACP_OIDC_AUDIENCE"


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
    summary.add_argument("--cost", type=Path,
                         help="infracost breakdown --format json output (informational; may be missing)")
    summary.add_argument("--step", action="append", default=[], metavar="NAME=OUTCOME", help="step outcome (repeatable)")
    summary.add_argument("--commit", default="", help="head commit shown in the comment")
    summary.add_argument("--run-url", default="", help="workflow run URL shown in the comment")
    summary.add_argument("--markdown-out", type=Path, required=True)
    summary.add_argument("--json-out", type=Path, required=True)

    pins = sub.add_parser("verify-pins", help="verify module pins; serve unpublished tags from a local mirror")
    pins.add_argument("stack_dirs", type=Path, nargs="+")
    pins.add_argument("--mirror", type=Path, required=True, help="bare mirror for unpublished tags (created if needed)")
    pins.add_argument("--trusted-ref", default=DEFAULT_TRUSTED_REF, help="unpublished pins must be on this ref")
    pins.add_argument(
        "--allow-unpublished",
        action="store_true",
        help="serve unpublished tags from the mirror (only while IACP_PIN_BOOTSTRAP / ADR-0009 is in force)",
    )
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

    sarif = sub.add_parser("sarif", help="rewrite a stack's TFLint or Checkov SARIF to repository-relative paths")
    sarif.add_argument("stack_dir", type=Path)
    sarif.add_argument("--in", dest="input", type=Path, required=True, help="SARIF written inside the stack root")
    sarif.add_argument("--out", type=Path, required=True)
    sarif.add_argument("--external-dir", action="append", default=[],
                       help="Checkov --external-modules-download-path (repeatable)")

    smoke = sub.add_parser("smoke", help="post-apply smoke test: declared outputs and iacp:* tags (state and live)")
    smoke.add_argument("stack_dir", type=Path)
    smoke.add_argument("--plan", type=Path, required=True, help="terraform show -json tfplan (declared outputs)")
    smoke.add_argument("--outputs", type=Path, required=True, help="terraform output -json")
    smoke.add_argument("--state", type=Path, required=True, help="terraform show -json after apply")
    smoke.add_argument("--endpoint-url", default=os.environ.get("AWS_ENDPOINT_URL", ""),
                       help="LocalStack or loopback endpoint for the live probes (default: $AWS_ENDPOINT_URL); "
                            "required: an empty or other endpoint is refused, so the probes never reach real AWS")
    smoke.add_argument("--region", default="", help="default: region in iacp.stack.json")
    smoke.add_argument("--markdown-out", type=Path)
    smoke.add_argument("--json-out", type=Path)

    report = sub.add_parser("report", help="POST one stage report to $IACP_API_URL/v1/runs")
    report.add_argument("stack_dir", type=Path)
    report.add_argument("--pipeline", required=True, choices=("validate", "apply", "drift", "ttl_destroy"))
    report.add_argument("--stage", required=True, help="stage name; a passed 'summary' finishes the run")
    report.add_argument("--status", help="in_progress|passed|failed|cancelled or a GitHub outcome "
                                         "(default: from --summary, failed when it is missing)")
    report.add_argument("--summary", type=Path, help="`summary --json-out` document: plan and policy blocks")
    report.add_argument("--outputs", type=Path, help="`terraform output -json`; sensitive values are redacted")
    report.add_argument("--terraform-exit-code", type=int)
    report.add_argument("--error-message", default="", help="error.message for a failed stage")
    report.add_argument("--error-class", default="permanent", choices=("transient", "permanent"))
    report.add_argument("--request-short-id", default="", help="default: request_id in iacp.stack.json")
    report.add_argument("--fencing-token", type=int,
                        help="the stack lock's token (default for validate: fencing_token in iacp.stack.json)")
    _api_arguments(report)
    report.add_argument("--warn-on-error", action="store_true",
                        help="report failures are ::warning:: annotations and exit 0 (the report never fails the job)")
    report.add_argument("--dry-run", action="store_true", help="print the payload instead of sending it")

    lock = sub.add_parser("lock", help="claim or release the platform stack lock (/v1/internal/stacks/{id}/lock)")
    lock.add_argument("action", choices=("acquire", "release"))
    lock.add_argument("--stack-id", required=True, help="stack UUID")
    lock.add_argument("--request-id", required=True, help="request UUID")
    lock.add_argument("--fencing-token", type=int,
                      help="token from acquire (release only); without it, release whatever lock this request holds")
    _api_arguments(lock)
    lock.add_argument(
        "--github-output",
        type=str,
        default=os.environ.get("GITHUB_OUTPUT"),
        help="append claimed= and fencing_token= here (default: $GITHUB_OUTPUT)",
    )
    return parser


def _api_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--api-url", default=os.environ.get(API_URL_ENV, ""), help=f"default: ${API_URL_ENV}")
    parser.add_argument("--audience", default=os.environ.get(AUDIENCE_ENV, ""),
                        help=f"GitHub OIDC audience (default: ${AUDIENCE_ENV}); without OIDC, $IACP_INTERNAL_TOKEN is used")
    parser.add_argument("--skip-if-unconfigured", action="store_true",
                        help=f"exit 0 with a ::notice:: when ${API_URL_ENV} is empty (the local-first default)")


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
        cost=parse_infracost(_read_json(args.cost)),
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
    verifier = ModulePinVerifier(
        GitCli(args.repo_root, args.remote), args.mirror, args.trusted_ref, allow_unpublished=args.allow_unpublished
    )
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


def run_sarif(args: argparse.Namespace) -> int:
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    doc = _read_json(args.input)
    if doc is None:
        raise SarifError(f"{args.input} is missing or is not JSON")
    # Both spellings: a tool may print the resolved path (/private/tmp on macOS).
    dirs = [str(p) for d in args.external_dir for p in dict.fromkeys((Path(d), Path(d).resolve()))]
    out = rebase_sarif(doc, meta.ref.path, dirs, str(Path(args.repo_root).resolve()))
    args.out.write_text(json.dumps(out, indent=1) + "\n", encoding="utf-8")
    print(f"{meta.ref.path}: {result_count(out)} SARIF result(s) -> {args.out}")
    return 0


def run_smoke(args: argparse.Namespace) -> int:
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    # First: refuses an empty or non-LocalStack endpoint before anything else runs.
    reader = AwsCliTagReader(args.endpoint_url, args.region or meta.region)
    docs = {name: _read_json(getattr(args, name)) for name in ("plan", "outputs", "state")}
    unusable = [f"--{name} {getattr(args, name)}" for name, doc in docs.items() if not isinstance(doc, dict)]
    if unusable:
        raise ValueError(f"missing or not a JSON object: {', '.join(unusable)}")
    result = SmokeTester(reader).run(meta, docs["plan"], docs["outputs"], docs["state"])
    if args.markdown_out:
        args.markdown_out.write_text(result.render_markdown(), encoding="utf-8")
    if args.json_out:
        args.json_out.write_text(json.dumps(result.to_dict(), indent=2) + "\n", encoding="utf-8")
    for check in result.checks:
        detail = f": {check.detail}" if check.detail else ""
        print(f"{'ok  ' if check.passed else 'FAIL'} {check.kind} {check.subject}{detail}")
    if not result.ok:
        print(f"::error title=Smoke test failed::{annotation(f'{meta.ref.path}: {len(result.failures)} failed check(s)')}")
    return 0 if result.ok else 1


def _build_report(args: argparse.Namespace, env: Mapping[str, str]) -> RunReport:
    """Assemble the report from the stack file, the pipeline's files and the runner env."""
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    summary = _read_json(args.summary)
    summary = summary if isinstance(summary, dict) else None
    outputs = _read_json(args.outputs)
    if args.outputs is not None and not isinstance(outputs, dict):
        raise ReportError(f"{args.outputs} is missing or is not a terraform output -json object")
    token = args.fencing_token
    if token is None and args.pipeline == "validate":
        token = meta.fencing_token
    status = stage_status(args.status) if args.status else summary_status(summary)
    return RunReport(
        pipeline=args.pipeline,
        stack_path=meta.ref.path,
        stage=args.stage,
        status=status,
        github=GitHubRun.from_env(env),
        request_short_id=args.request_short_id or meta.request_id,
        fencing_token=token,
        plan=plan_block(summary) if summary else None,
        policy=policy_block(summary) if summary else None,
        outputs=redact_outputs(outputs) if isinstance(outputs, dict) else None,
        terraform_exit_code=args.terraform_exit_code,
        error=error_block(args.error_message, args.error_class),
    )


def _client(args: argparse.Namespace, env: Mapping[str, str]) -> IacpApiClient:
    # One OIDC token per request: the API rejects a reused jti (lock release reads the
    # lock first, and retries resend), so the client mints a fresh token for each call.
    credential = FreshCredentials.start(TokenSource(env, stdout=sys.stdout), args.audience)
    if credential is None:
        raise ApiError(
            f"no credential for {args.api_url}: grant the job id-token: write and set {AUDIENCE_ENV}, "
            "or provide IACP_INTERNAL_TOKEN"
        )
    return IacpApiClient(args.api_url, credential)


def _unconfigured(args: argparse.Namespace, what: str) -> bool:
    """True (after a notice) when the API URL is empty and the caller asked to skip."""
    if args.api_url.strip():
        return False
    if not args.skip_if_unconfigured:
        raise ValueError(f"{API_URL_ENV} is not set; pass --api-url or --skip-if-unconfigured")
    print(f"::notice title=Platform API not configured::{what} skipped: {API_URL_ENV} is empty (local-first, ADR-0009)")
    return True


def run_report(args: argparse.Namespace) -> int:
    env = os.environ
    try:
        payload = _build_report(args, env).to_payload()
        if args.dry_run:
            print(json.dumps(payload, indent=2))
            return 0
        if _unconfigured(args, f"run report {args.pipeline}/{args.stage}"):
            return 0
        resp = _client(args, env).post_run(payload)
    except (ReportError, ApiError, StackMetadataError, ValueError) as exc:
        if not args.warn_on_error:
            raise
        print(f"::warning title=Run report not sent::{annotation(str(exc))}")
        return 0
    body = resp.body
    print(f"reported {args.pipeline}/{args.stage}={payload['status']}: run {body.get('run_id')} "
          f"is {body.get('run_status')} (request status {body.get('request_status')})")
    return 0


def run_lock(args: argparse.Namespace) -> int:
    out = Path(args.github_output) if args.github_output else None
    if _unconfigured(args, f"stack lock {args.action}"):
        _write_outputs(out, {"attempted": "false", "claimed": "false"})
        return 0
    client = _client(args, os.environ)
    if args.action == "acquire":
        # Recorded before the call: when the grant commits but every response is
        # lost (timeout), the always() release still runs and finds the lock.
        _write_outputs(out, {"attempted": "true"})
        claim = client.acquire_lock(args.stack_id, args.request_id)
        _write_outputs(out, {"claimed": "true", "fencing_token": str(claim.fencing_token)})
        print(f"stack lock {claim.outcome} for request {claim.request_id}: fencing token {claim.fencing_token}, "
              f"expires {claim.expires_at}")
        return 0
    token = args.fencing_token
    if token is None:
        token = _own_lock_token(client, args.stack_id, args.request_id)
        if token is None:
            return 0
    if token <= 0:
        raise ValueError("--fencing-token must be a positive integer")
    body = client.release_lock(args.stack_id, args.request_id, token).body
    promoted = (body.get("promoted") or {}).get("request_id")
    print(f"stack lock released (token {token}); promoted: {promoted or 'none'}")
    return 0


def _own_lock_token(client: IacpApiClient, stack_id: str, request_id: str) -> int | None:
    """The fencing token of the stack lock if this request holds it, else None (nothing to release).

    Used when the claim's response was lost: the lock may or may not have been granted.
    """
    current = client.get_lock(stack_id)
    if current is None:
        print("stack lock not held; nothing to release")
        return None
    if str(current.get("request_id", "")).lower() != request_id.strip().lower():
        print("stack lock is held by another request; nothing to release")
        return None
    token = current.get("fencing_token")
    if not isinstance(token, int) or isinstance(token, bool):
        raise ApiError("stack lock response carried no fencing token")
    return token


def annotation(text: str) -> str:
    """Escape text for a workflow command message so it cannot start a new command. Pure."""
    return text.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")


def _write_outputs(path: Path | None, values: Mapping[str, str]) -> None:
    if path is None:
        return
    with path.open("a", encoding="utf-8") as fh:
        fh.writelines(f"{k}={v}\n" for k, v in values.items())


COMMANDS = {
    "detect": run_detect,
    "context": run_context,
    "summary": run_summary,
    "verify-pins": run_verify_pins,
    "verify-modules": run_verify_modules,
    "sarif": run_sarif,
    "smoke": run_smoke,
    "report": run_report,
    "lock": run_lock,
}


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return COMMANDS[args.command](args)
    except (ReportError, ApiError) as exc:
        # These can quote server responses; keep them on one line.
        print(f"error: {annotation(str(exc))}", file=sys.stderr)
        return 1
    except (DetectionError, StackMetadataError, PinError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
