"""Command-line entry point: ``python -m tools.stack_pipeline <command>``."""

from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
import uuid
from pathlib import Path
from typing import Any, Mapping, Sequence

from .adapters import AwsCliTagReader, FileSystemStackRepository, GitCli, GitDiff
from .api_client import STACK_PATH_RE, ApiError, FreshCredentials, IacpApiClient, TokenSource
from .convergence import plan_problem, residual_changes
from .cost import fold_cost, parse_infracost, redact_plan
from .drift import MAX_CHANGED, DriftOutcome, build_outcome, outcome_from_dict
from .detection import DEFAULT_MAX_STACKS, DetectionError, DetectionPolicy, DetectionResult, StackDetector
from .models import StackMetadataError, StackRef
from .module_install import MANIFEST, ModuleInstallVerifier, parse_manifest
from .module_pins import DEFAULT_REPO_URL, DEFAULT_TRUSTED_REF, ModulePinVerifier, PinError, Resolution, insteadof_config
from .plan_summary import render_markdown, summarize
from .stack_guard import LOCK_FILE, Violation, check_entries, check_stack, lock_allowlist
from .policy_context import ContextError, build_budget_context, build_context
from .sarif import SarifError, rebase_sarif, result_count
from .smoke import SmokeTester
from .state_surgery import (
    SurgeryInputError,
    SurgeryRequest,
    SurgeryResult,
    SurgeryRunner,
    TerraformRunner,
    build_report,
    error_excerpt,
    error_signals,
    parse_request,
    refused_report,
    with_triggering_actor,
)
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

DRIFT_MAX_STACKS = 256  # one drift.yml dispatch (docs/06 section 4)
MAX_LOG_BYTES = 1_000_000  # how much of terraform's log `drift scan --error-file` reads
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
    context.add_argument("stack_dir", type=Path, nargs="?",
                         help="stack root with iacp.stack.json (not used with --budget-gate)")
    context.add_argument("--allowed-region", action="append", default=[], help="override the policy default (repeatable)")
    context.add_argument("--budget-gate", action="store_true",
                         help="the budget gate's context (policies/opa/budget.rego): identity from --stack-path, "
                              "budget from --api-context, the monthly cost from --cost; a missing estimate counts")
    context.add_argument("--stack-path", help="environments/<env>/<team>/<stack>, from the detect job (--budget-gate)")
    context.add_argument("--cost", type=Path,
                         help="infracost breakdown --format json output of the isolated cost job (--budget-gate); "
                              "absent or unusable means no estimate")
    context.add_argument("--api-context", type=Path,
                         help="`fetch-budget-context` output (--budget-gate); absent means no budget is configured")

    guard = sub.add_parser("check-stack",
                           help="static check of a stack root's HCL before init and plan (no plan-time code)")
    guard.add_argument("stack_dir", type=Path)
    guard.add_argument("--lock-from", type=Path, metavar="DIR",
                       help="a checkout of the base commit: every .terraform.lock.hcl under DIR/environments is the "
                            "allow-list of provider versions and hashes the stack's lock may carry")

    fetch = sub.add_parser("fetch-budget-context",
                           help="GET /v1/internal/budget-context for a stack root and write it to a file")
    fetch.add_argument("--stack-path", required=True, help="environments/<env>/<team>/<stack>")
    fetch.add_argument("--out", type=Path, required=True)
    _api_arguments(fetch)

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

    cost_input = sub.add_parser("cost-input", help="plan JSON for the separate Infracost job, sensitive values nulled")
    cost_input.add_argument("--plan", type=Path, required=True, help="terraform show -json tfplan")
    cost_input.add_argument("--out", type=Path, required=True)

    fold = sub.add_parser("fold-cost", help="add an Infracost estimate to a rendered summary (in place)")
    fold.add_argument("--summary-json", type=Path, required=True, help="summary --json-out document")
    fold.add_argument("--markdown", type=Path, required=True, help="summary --markdown-out document")
    fold.add_argument("--cost", type=Path, required=True, help="infracost breakdown --format json output")

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

    converged = sub.add_parser("converged", help="after apply: fail unless a re-plan is empty (emulator tag drops excepted)")
    converged.add_argument("--plan", type=Path, required=True, help="terraform show -json of the re-plan")

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
    report.add_argument("--error-log", type=Path,
                        help="Terraform log of a failed stage; its error excerpt becomes error.message when "
                             "--error-message is empty (the backend classifies it: transient, config or infra)")
    report.add_argument("--error-class", default="permanent", choices=("transient", "permanent"))
    report.add_argument("--request-short-id", default="", help="default: request_id in iacp.stack.json")
    report.add_argument("--fencing-token", type=int,
                        help="the stack lock's token (default for validate: fencing_token in iacp.stack.json)")
    _api_arguments(report)
    report.add_argument("--warn-on-error", action="store_true",
                        help="report failures are ::warning:: annotations and exit 0 (the report never fails the job)")
    report.add_argument("--dry-run", action="store_true", help="print the payload instead of sending it")

    drift = sub.add_parser("drift", help="nightly drift check: list stacks, summarise a refresh-only plan, report it")
    drift_sub = drift.add_subparsers(dest="drift_action", required=True)
    d_list = drift_sub.add_parser("list", help="matrix of the active stack roots under environments/ (JSON)")
    d_list.add_argument("--paths", default="", help="JSON array of stack paths to check instead of all (workflow_dispatch input)")
    d_list.add_argument("--max-stacks", type=int, default=DRIFT_MAX_STACKS,
                        help="matrix limit; the least recently listed beyond it wait for the next run")
    d_list.add_argument("--github-output", type=str, default=os.environ.get("GITHUB_OUTPUT"),
                        help="append matrix= and count= here (default: $GITHUB_OUTPUT)")
    d_scan = drift_sub.add_parser("scan", help="classify one stack's `plan -refresh-only -detailed-exitcode` and write the result file")
    d_scan.add_argument("stack_dir", type=Path)
    d_scan.add_argument("--exit-code", type=int, required=True, help="terraform's exit code (0, 1 or 2)")
    d_scan.add_argument("--plan", type=Path, help="terraform show -json of the refresh-only plan (needed for exit 0 and 2)")
    d_scan.add_argument("--error-message", default="", help="what failed, for exit 1 (only its `Error:` line is kept)")
    d_scan.add_argument("--error-file", type=Path,
                        help="terraform's whole log for exit 1; only its first `Error:` line is kept, redacted and capped")
    d_scan.add_argument("--out", type=Path, required=True, help="result file (plan values never reach it)")
    d_report = drift_sub.add_parser("report", help="POST every scan result under --results-dir to $IACP_API_URL/v1/internal/drift")
    d_report.add_argument("--results-dir", type=Path, required=True, help="directory of result files written by `drift scan`")
    _api_arguments(d_report)
    d_report.add_argument("--dry-run", action="store_true", help="print the payloads instead of sending them")

    lock = sub.add_parser("lock", help="claim or release the platform stack lock (/v1/internal/stacks/{id}/lock)")
    lock.add_argument("action", choices=("acquire", "release"))
    lock.add_argument("--stack-id", help="stack UUID (acquire: resolved through --stack-path when omitted)")
    lock.add_argument("--request-id", help="request UUID (acquire: resolved through --stack-path when omitted)")
    lock.add_argument("--stack-path",
                      help="environments/<env>/<team>/<stack>. acquire resolves the stack and request UUIDs, and the "
                           "approved plan hash, with GET /v1/internal/requests/by-path when they are not passed; the "
                           "lookup fails closed")
    lock.add_argument("--head-sha", default="",
                      help="commit that triggered the run; the API refuses a request that was merged as another commit")
    lock.add_argument("--fencing-token", type=int,
                      help="token from acquire (release only); without it, release whatever lock this request holds")
    _api_arguments(lock)
    lock.add_argument(
        "--github-output",
        type=str,
        default=os.environ.get("GITHUB_OUTPUT"),
        help="append attempted=, claimed= and fencing_token= here, and stack_id=, request_id= and "
             "approved_plan_hash= when acquire resolved them (default: $GITHUB_OUTPUT)",
    )
    surgery = sub.add_parser("state-surgery", help="break-glass state surgery: validate inputs, run, report the audit")
    surgery.add_argument("action", choices=("validate", "run", "report"))
    surgery.add_argument("--request", type=Path,
                         help="validated request JSON (validate writes it; run and report read it)")
    surgery.add_argument("--result", type=Path, help="result JSON (run writes it; report reads it)")
    _api_arguments(surgery)
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


def _read_bounded(path: Path, limit: int) -> str | None:
    """At most ``limit`` bytes of a text file (never the whole file), or None when it is absent."""
    if not path.is_file():
        return None
    with path.open("rb") as fh:
        return fh.read(limit).decode("utf-8", errors="replace")


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
    if args.budget_gate:
        return _run_budget_context(args)
    if args.stack_dir is None or args.stack_path or args.cost or args.api_context:
        raise ValueError("context needs a stack directory; --stack-path, --cost and --api-context go with --budget-gate")
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    print(json.dumps(build_context(meta, args.allowed_region), indent=2))
    return 0


def _run_budget_context(args: argparse.Namespace) -> int:
    """The budget gate's context. It reads no file from the stack's tree: identity is the path,
    the budget is the API's, and the estimate is the cost job's (see policy_context)."""
    ref = StackRef.from_root(args.stack_path or "")
    if ref is None or args.stack_dir is not None:
        raise ValueError("--budget-gate needs --stack-path environments/<env>/<team>/<stack> and no stack directory")
    api_document = None
    if args.api_context is not None and args.api_context.is_file():
        api_document = _read_json(args.api_context)
        if not isinstance(api_document, dict):
            raise ContextError(f"{args.api_context} is not a JSON object")
    estimate = parse_infracost(_read_json(args.cost))
    document = build_budget_context(ref, estimate, api_document, args.allowed_region)
    context = document["iacp"]["context"]
    print(json.dumps(document, indent=2))
    sys.stderr.write(
        f"budget gate for {ref.path}: budget {'set' if context.get('budget') else 'not set'}, "
        f"estimate {context.get('estimated_monthly_cost_usd', 'missing')}\n"
    )
    return 0


MAX_GUARDED_FILE_BYTES = 1 << 20


def _read_stack_files(root: Path) -> tuple[dict[str, str], list[Violation]]:
    """The scannable top-level files of a stack root, and a violation for every entry that is not
    a plain allow-listed file (a directory, a symlink, an oversized or non-UTF-8 file). I/O adapter."""
    files: dict[str, str] = {}
    refused: list[Violation] = []
    if not root.is_dir():
        raise ValueError(f"{root} is not a directory")
    kinds: dict[str, str] = {}
    for entry in sorted(root.iterdir()):
        name = entry.name
        kinds[name] = ("symlink" if entry.is_symlink() else "dir" if entry.is_dir() else
                       "file" if entry.is_file() else "other")
    refused += check_entries(kinds)
    for entry in sorted(root.iterdir()):
        name = entry.name
        if kinds[name] == "file" and (name.endswith((".tf", ".tf.json")) or name == LOCK_FILE):
            if entry.stat().st_size > MAX_GUARDED_FILE_BYTES:
                refused.append(Violation(name, 1, "size", f"the file is larger than {MAX_GUARDED_FILE_BYTES} bytes"))
                continue
            try:
                files[name] = entry.read_text(encoding="utf-8")
            except UnicodeDecodeError:
                refused.append(Violation(name, 1, "unreadable", "the file is not UTF-8"))
    return files, refused


def _read_lock_pins(base: Path) -> dict:
    """The lock allow-list from every stack lock file of the base checkout. I/O adapter."""
    locks = sorted((base / "environments").glob(f"*/*/*/{LOCK_FILE}"))
    texts = []
    for p in locks:
        if p.is_file() and not p.is_symlink() and p.stat().st_size <= MAX_GUARDED_FILE_BYTES:
            texts.append(p.read_text(encoding="utf-8", errors="replace"))
    return lock_allowlist(texts)


def run_check_stack(args: argparse.Namespace) -> int:
    files, refused = _read_stack_files(args.stack_dir)
    pins = None
    if args.lock_from is not None:
        pins = _read_lock_pins(args.lock_from)
        if not pins:
            raise ValueError(f"no provider pins found under {args.lock_from}/environments: the lock cannot be checked")
    violations = refused + check_stack(files, pins=pins)
    for v in violations:
        print(f"::error file={args.stack_dir / v.path},line={v.line}::{annotation(v.render())}")
    if violations:
        print(f"error: {len(violations)} problem(s) in {args.stack_dir}: the stack root may only call pinned "
              "platform modules (no resources, data sources, provisioners, other providers or backends)",
              file=sys.stderr)
        return 1
    print(f"{args.stack_dir}: {len(files)} file(s) checked, no plan-time code")
    return 0


def run_fetch_budget_context(args: argparse.Namespace) -> int:
    if _unconfigured(args, "budget context"):
        return 0
    document = _client(args, os.environ).get_budget_context(args.stack_path)
    args.out.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
    print(f"wrote the budget context of {args.stack_path} to {args.out}")
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
    if data is None and not meta.modules:
        # terraform init writes no modules.json for a root with no module calls (a destroy
        # root). With nothing pinned, a missing manifest can only mean there is nothing to check.
        print(f"{meta.ref.key}: no module calls and no pins; nothing to verify")
        return 0
    if data is None:
        raise PinError(f"{manifest} is missing or unreadable; run terraform init first")
    verifier = ModuleInstallVerifier(GitCli(args.repo_root), args.repo_url)
    for line in verifier.verify(Path(args.stack_dir), meta, parse_manifest(data)):
        print(line)
    return 0


def run_cost_input(args: argparse.Namespace) -> int:
    plan = _read_json(args.plan)
    if not isinstance(plan, dict):
        raise ValueError(f"{args.plan} is missing or is not a plan JSON object")
    args.out.write_text(json.dumps(redact_plan(plan)) + "\n", encoding="utf-8")
    print(f"wrote the redacted plan for Infracost to {args.out}")
    return 0


def run_fold_cost(args: argparse.Namespace) -> int:
    """Informational: a missing or unreadable estimate leaves the summary as it is."""
    estimate = parse_infracost(_read_json(args.cost))
    summary = _read_json(args.summary_json)
    markdown = _read_text(args.markdown)
    if estimate is None or not isinstance(summary, dict) or markdown is None:
        print("::notice title=No cost estimate::the summary is left without a cost line "
              "(no Infracost output, or no summary to add it to)")
        return 0
    folded, md = fold_cost(summary, markdown, estimate)
    args.summary_json.write_text(json.dumps(folded, indent=2) + "\n", encoding="utf-8")
    args.markdown.write_text(md, encoding="utf-8")
    print(estimate.render_markdown())
    return 0


def run_sarif(args: argparse.Namespace) -> int:
    doc = _read_json(args.input)
    if doc is None:
        raise SarifError(f"{args.input} is missing or is not JSON")
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    # Both spellings: a tool may print the resolved path (/private/tmp on macOS).
    dirs = [str(p) for d in args.external_dir for p in dict.fromkeys((Path(d), Path(d).resolve()))]
    out = rebase_sarif(doc, meta.ref.path, dirs, str(Path(args.repo_root).resolve()))
    args.out.write_text(json.dumps(out, indent=1) + "\n", encoding="utf-8")
    print(f"{meta.ref.path}: {result_count(out)} SARIF result(s) -> {args.out}")
    return 0


def run_converged(args: argparse.Namespace) -> int:
    doc = _read_json(args.plan)
    if not isinstance(doc, dict):
        raise ValueError(f"missing or not a JSON object: --plan {args.plan}")
    problem = plan_problem(doc)
    if problem:
        raise ValueError(f"--plan {args.plan}: {problem}")
    residual = residual_changes(doc)
    for line in residual:
        print(f"not converged: {line}")
    if residual:
        print(f"::error title=Stack is not idempotent::{annotation(f'{len(residual)} change(s) left after apply')}")
    else:
        print("converged: the re-plan is empty")
    return 1 if residual else 0


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


def run_drift(args: argparse.Namespace) -> int:
    return {"list": _drift_list, "scan": _drift_scan, "report": _drift_report}[args.drift_action](args)


def _drift_list(args: argparse.Namespace) -> int:
    """Every active stack root under environments/ (or the dispatched subset), as a matrix.

    A stack file that says it is not active (a destroyed stack keeps an empty marked root)
    is left out. With more than --max-stacks stacks the matrix is cut (alphabetical) and a
    warning says so.
    """
    repo = FileSystemStackRepository(args.repo_root)
    active = {m.ref.path: m for ref in repo.all_refs() if (m := repo.load(ref)) is not None and m.lifecycle == "active"}
    wanted = _drift_paths(args.paths)
    if wanted is not None:
        unknown = sorted(set(wanted) - set(active))
        if unknown:
            raise ValueError(f"not an active stack root: {', '.join(unknown)}")
        active = {p: active[p] for p in wanted}
    stacks = [active[p] for p in sorted(active)]
    if len(stacks) > args.max_stacks:
        print(f"::warning title=Drift matrix cut::{len(stacks)} active stacks, checking the first {args.max_stacks}")
        stacks = stacks[: args.max_stacks]
    matrix = [m.matrix_entry() for m in stacks]
    print(json.dumps({"count": len(matrix), "stacks": [m["path"] for m in matrix]}))
    _write_outputs(Path(args.github_output) if args.github_output else None,
                   {"matrix": json.dumps(matrix, separators=(",", ":")), "count": str(len(matrix))})
    return 0


def _drift_paths(raw: str) -> list[str] | None:
    """The dispatch input: a JSON array of stack paths, or None when empty. Checked before use,
    and never echoed (a dispatcher could put a workflow command in it)."""
    if not raw.strip():
        return None
    try:
        value = json.loads(raw)
    except json.JSONDecodeError:
        raise ValueError("stacks must be a JSON array of stack paths") from None
    if not isinstance(value, list) or not value or not all(isinstance(p, str) and STACK_PATH_RE.match(p) for p in value):
        raise ValueError("stacks must be a non-empty JSON array of environments/<env>/<team>/<stack> paths")
    return list(dict.fromkeys(value))


def _drift_scan(args: argparse.Namespace) -> int:
    meta = FileSystemStackRepository(args.repo_root).load_dir(args.stack_dir)
    plan = _read_json(args.plan)
    # The whole log is read (bounded) rather than a pre-cut window: the summary is the first
    # `Error:` line wherever it is, and nothing else of the log is kept.
    log = _read_bounded(args.error_file, MAX_LOG_BYTES) if args.error_file is not None else None
    message = log if log is not None else args.error_message
    outcome = build_outcome(meta.ref.path, args.exit_code, plan if isinstance(plan, dict) else None, message)
    errs = outcome.problems()
    if errs:
        raise ValueError("drift result is invalid: " + "; ".join(errs))
    args.out.write_text(json.dumps(outcome.to_dict(), indent=2) + "\n", encoding="utf-8")
    print(f"{meta.ref.path}: {_describe(outcome)}")
    if outcome.result == "error":
        print(f"::warning title=Drift check failed::{annotation(f'{meta.ref.path}: {outcome.error}')}")
    return 0


def _describe(o: DriftOutcome) -> str:
    if o.result == "drifted":
        s = o.summary
        shown = f", first {MAX_CHANGED} listed" if s["drifted"] > MAX_CHANGED else ""
        return f"drifted ({s['drifted']} resources: {s['updated']} updated, {s['deleted']} deleted{shown})"
    return o.result if o.result == "clean" else f"error ({o.error})"


def _drift_report(args: argparse.Namespace) -> int:
    """Report each scan result for a stack the API lists as due. The API is asked once, then
    each report is sent with its own OIDC token. A stack the API does not list (it became
    busy, locked or inactive since the scan) is skipped: its result describes a state that
    is mid-change. A rejected report fails the job after every other one was sent."""
    outcomes = _load_outcomes(args.results_dir)
    if args.dry_run:
        gh = GitHubRun.from_env(os.environ)
        print(json.dumps([o.to_payload(gh) for o in outcomes], indent=2))
        return 0
    if _unconfigured(args, "drift reports"):
        return 0
    gh = GitHubRun.from_env(os.environ)
    payloads = {o.stack_path: o.to_payload(gh) for o in outcomes}  # everything is valid before anything is sent
    client = _client(args, os.environ)
    due = {s.path for s in client.drift_stacks(DRIFT_MAX_STACKS)}
    failed = 0
    for path in sorted(payloads):
        if path not in due:
            print(f"::notice title=Drift result skipped::{path} is not due according to the API (busy, locked or inactive)")
            continue
        try:
            body = client.post_drift(payloads[path]).body
        except ApiError as exc:
            if exc.code == "stack_not_checkable":  # became busy between the list and the report
                print(f"::notice title=Drift result skipped::{path}: {annotation(str(exc))}")
                continue
            failed += 1
            print(f"::error title=Drift report rejected::{annotation(f'{path}: {exc}')}")
            continue
        print(f"reported {path}: {body.get('result')} -> report {body.get('report_id')}"
              f"{' (replay)' if body.get('replayed') else ''}, resolution status {body.get('status')}")
    for path in sorted(due - set(payloads)):
        print(f"::warning title=No drift result::{path} is due but produced no scan result")
    return 1 if failed else 0


def _load_outcomes(directory: Path) -> list[DriftOutcome]:
    """Every ``*.json`` result under directory (not recursive), validated strictly. A file that
    is not a valid result fails the command: it crossed a job boundary and is never trusted.
    Two files for one stack are refused."""
    outcomes: dict[str, DriftOutcome] = {}
    for file in sorted(Path(directory).glob("*.json")):
        raw = _read_json(file)
        if raw is None:
            raise ReportError(f"{file.name} is not valid JSON")
        outcome = outcome_from_dict(raw)
        if outcome.stack_path in outcomes:
            raise ReportError(f"two drift results for {outcome.stack_path}")
        outcomes[outcome.stack_path] = outcome
    return list(outcomes.values())


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
    message, signals = _error_message(args, status)
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
        error=error_block(message, args.error_class, signals),
    )


def _error_message(args: argparse.Namespace, status: str) -> tuple[str, list[str] | None]:
    """--error-message, else the redacted excerpt of --error-log for a failed stage, with its
    classification signals (sent as their own field)."""
    if args.error_message or status != "failed" or args.error_log is None:
        return args.error_message, None
    text = _read_text(args.error_log)
    return (error_excerpt(text), error_signals(text)) if text else ("", None)


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
        stack_id, request_id = _lock_ids(client, args, out)
        # Recorded before the call: when the grant commits but every response is
        # lost (timeout), the always() release still runs and finds the lock.
        _write_outputs(out, {"attempted": "true"})
        claim = client.acquire_lock(stack_id, request_id)
        _write_outputs(out, {"claimed": "true", "fencing_token": str(claim.fencing_token)})
        print(f"stack lock {claim.outcome} for request {claim.request_id}: fencing token {claim.fencing_token}, "
              f"expires {claim.expires_at}")
        return 0
    if not (args.stack_id and args.request_id):
        raise ValueError("lock release needs --stack-id and --request-id")
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


def _lock_ids(client: IacpApiClient, args: argparse.Namespace, out: Path | None) -> tuple[str, str]:
    """The stack and request UUIDs to lock for.

    With --stack-path the API is always asked (GET /v1/internal/requests/by-path), even when
    both ids were passed: the answer supplies the approved plan hash and proves the ids name
    the request that holds the stack lock and is merged. Ids passed next to --stack-path must
    agree with it. The answer is written to the step outputs (stack_id, request_id,
    approved_plan_hash) before the claim, so the release step and the plan-hash check can use
    it. A lookup that finds nothing raises, so the job fails instead of applying without the
    lock. Only without --stack-path are two passed ids used as they are (no lookup is possible).
    """
    if not args.stack_path:
        if args.stack_id and args.request_id:
            return args.stack_id, args.request_id
        raise ValueError("lock acquire needs --stack-id and --request-id, or --stack-path to resolve them")
    resolved = client.resolve_request(args.stack_path, args.head_sha)
    for given, found, what in ((args.stack_id, resolved.stack_id, "stack"), (args.request_id, resolved.request_id, "request")):
        if given and given.strip().lower() != found:
            raise ApiError(f"--{what}-id {given} is not the {what} the API resolved for {args.stack_path} ({found})")
    _write_outputs(out, {"stack_id": resolved.stack_id, "request_id": resolved.request_id,
                         "approved_plan_hash": resolved.approved_plan_hash})
    print(f"resolved {args.stack_path}: request {resolved.request_short_id or resolved.request_id}, "
          f"approved plan hash {resolved.approved_plan_hash or 'none'}")
    return resolved.stack_id, resolved.request_id


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


# Dispatch inputs reach the tool only through these variables (never through argv or a
# ${{ }} expression inside a run: block).
SURGERY_ENV = {"stack_path": "IN_STACK_PATH", "operation": "IN_OPERATION", "args": "IN_ARGS",
               "ticket": "IN_TICKET", "dry_run": "IN_DRY_RUN", "requested_by": "GITHUB_ACTOR"}


def surgery_inputs(env: Mapping[str, str]) -> dict[str, str]:
    """The raw dispatch inputs from the environment. Pure over the mapping."""
    return {k: env.get(v, "") for k, v in SURGERY_ENV.items()}


def run_state_surgery(args: argparse.Namespace) -> int:
    env = os.environ
    if args.action == "validate":
        return _surgery_validate(args, env)
    if args.action == "run":
        return _surgery_run(args)
    return _surgery_report(args, env)


def _surgery_validate(args: argparse.Namespace, env: Mapping[str, str]) -> int:
    if args.request is None:
        raise ValueError("state-surgery validate needs --request (where to write the validated request)")
    try:
        req = parse_request(surgery_inputs(env))
    except SurgeryInputError as exc:
        # The problems name inputs, never their values: nothing the dispatcher typed reaches the log.
        for problem in exc.problems:
            print(f"::error title=state-surgery input refused::{annotation(problem)}")
        return 1
    args.request.write_text(json.dumps(req.to_dict(), indent=2), encoding="utf-8")
    print(f"validated: terraform {req.operation} on {req.stack_path} with {len(req.args)} argument(s), "
          f"ticket {req.ticket}, requested by {req.requested_by}, dry run {str(req.dry_run).lower()}")
    return 0


def _surgery_run(args: argparse.Namespace) -> int:
    if args.request is None or args.result is None:
        raise ValueError("state-surgery run needs --request and --result")
    req = SurgeryRequest.from_dict(_read_json(args.request) or {})
    stack_dir = (args.repo_root / req.stack_path).resolve()
    if not (stack_dir / "iacp.stack.json").is_file():
        raise ValueError(f"{req.stack_path} is not a stack root")
    # Terraform output is data, not workflow commands: pause command processing while it runs.
    token = uuid.uuid4().hex
    print(f"::stop-commands::{token}", flush=True)
    try:
        result = SurgeryRunner(TerraformRunner(stack_dir)).run(req)
    finally:
        print(f"::{token}::", flush=True)
    args.result.write_text(json.dumps(result.to_dict(), indent=2), encoding="utf-8")
    print(f"state surgery {result.outcome}: exit code {result.exit_code}, post-step plan exit code {result.plan_exit_code}")
    return 0 if result.outcome == "succeeded" else 1


def _surgery_report(args: argparse.Namespace, env: Mapping[str, str]) -> int:
    run = GitHubRun.from_env(env).to_dict()
    doc = _read_json(args.request) if args.request is not None else None
    if isinstance(doc, dict):
        req = SurgeryRequest.from_dict(doc)
        result_doc = _read_json(args.result) if args.result is not None else None
        result = SurgeryResult.from_dict(result_doc) if isinstance(result_doc, dict) else SurgeryResult(outcome="failed")
        payload = build_report(req, result, run)
    else:
        payload = refused_report(surgery_inputs(env), run)
    payload = with_triggering_actor(payload, env.get("GITHUB_TRIGGERING_ACTOR", ""))
    if _unconfigured(args, "state surgery audit report"):
        print(json.dumps(payload, indent=2))
        return 0
    body = _client(args, env).post_state_surgery(payload).body
    print(f"state surgery recorded on stack {body.get('stack_id')} (duplicate: {body.get('duplicate')})")
    return 0


COMMANDS = {
    "detect": run_detect,
    "context": run_context,
    "fetch-budget-context": run_fetch_budget_context,
    "check-stack": run_check_stack,
    "summary": run_summary,
    "verify-pins": run_verify_pins,
    "verify-modules": run_verify_modules,
    "cost-input": run_cost_input,
    "fold-cost": run_fold_cost,
    "sarif": run_sarif,
    "smoke": run_smoke,
    "converged": run_converged,
    "report": run_report,
    "lock": run_lock,
    "state-surgery": run_state_surgery,
    "drift": run_drift,
}


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return COMMANDS[args.command](args)
    except (ReportError, ApiError) as exc:
        # These can quote server responses; keep them on one line.
        print(f"error: {annotation(str(exc))}", file=sys.stderr)
        return 1
    except (DetectionError, StackMetadataError, PinError, ContextError, SurgeryInputError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
