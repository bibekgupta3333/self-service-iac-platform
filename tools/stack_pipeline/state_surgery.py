"""Break-glass state surgery (docs/06-lifecycle-management.md section 2.3, WBS 8.7).

``state-surgery.yml`` is the only place Terraform state is edited by hand. This module
holds its logic, all pure except ``TerraformRunner`` and ``state_digest``:

* ``parse_request`` validates the workflow_dispatch inputs strictly: the stack path
  pattern, the operation allow-list (``state rm``, ``state mv``, ``import``,
  ``force-unlock``) and each argument (a resource address, a resource id or a lock
  id). Error messages name the input, never echo its value, so a crafted input cannot
  reach a workflow command (``::...::``) through the log.
* ``terraform_argv`` maps a validated request to one argv list (never a shell string).
* ``build_report`` / ``refused_report`` build the body of POST
  /v1/internal/state-surgery, mirrored by ``recovery.SurgeryReport`` in the backend.

The rules mirror backend/internal/recovery/surgery.go; tests on both sides use the same
cases.
"""

from __future__ import annotations

import hashlib
import re
import subprocess
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Mapping, Sequence

OP_STATE_RM = "state rm"
OP_STATE_MV = "state mv"
OP_IMPORT = "import"
OP_FORCE_UNLOCK = "force-unlock"
OPERATIONS = (OP_STATE_RM, OP_STATE_MV, OP_IMPORT, OP_FORCE_UNLOCK)
# Operations with a native -dry-run; for the others a dry run validates and runs nothing.
DRY_RUN_NATIVE = (OP_STATE_RM, OP_STATE_MV)

OUTCOME_SUCCEEDED = "succeeded"
OUTCOME_FAILED = "failed"
OUTCOME_REFUSED = "refused"

STACK_PATH_RE = re.compile(r"environments/[a-z]+/[a-z][a-z0-9-]{1,38}/[a-z][a-z0-9-]{1,40}")
_INDEX = r'(\[[0-9]{1,6}\]|\["[A-Za-z0-9_.:/-]{1,64}"\])?'
ADDRESS_RE = re.compile(
    r"(module\.[a-z_][a-z0-9_-]*" + _INDEX + r"\.)*(data\.)?[a-z][a-z0-9_]*\.[A-Za-z_][A-Za-z0-9_-]*" + _INDEX
)
IMPORT_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9:/_.@=+,-]{0,255}")
LOCK_ID_RE = re.compile(r"[0-9A-Fa-f][0-9A-Fa-f-]{7,63}")
TICKET_RE = re.compile(r"[A-Z][A-Z0-9]{1,9}-[0-9]{1,7}")
LOGIN_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9-]{0,38}")
MAX_ADDRESS = 256
MAX_ARGS_INPUT = 4_096


class SurgeryInputError(ValueError):
    """The dispatch inputs were refused. ``problems`` name inputs, never their values."""

    def __init__(self, problems: Sequence[str]) -> None:
        super().__init__("; ".join(problems))
        self.problems = tuple(problems)


@dataclass(frozen=True)
class SurgeryRequest:
    """A validated state surgery request."""

    stack_path: str
    operation: str
    args: tuple[str, ...]
    ticket: str
    requested_by: str
    dry_run: bool

    def to_dict(self) -> dict[str, Any]:
        return {"stack_path": self.stack_path, "operation": self.operation, "args": list(self.args),
                "ticket": self.ticket, "requested_by": self.requested_by, "dry_run": self.dry_run}

    @classmethod
    def from_dict(cls, doc: Mapping[str, Any]) -> SurgeryRequest:
        """Re-validate a request written by an earlier step (the file is not trusted blindly)."""
        args = doc.get("args")
        if not isinstance(args, list) or not all(isinstance(a, str) for a in args):
            raise SurgeryInputError(["args must be a list of strings"])
        return parse_request({
            "stack_path": str(doc.get("stack_path", "")), "operation": str(doc.get("operation", "")),
            "args": " ".join(args), "ticket": str(doc.get("ticket", "")),
            "requested_by": str(doc.get("requested_by", "")), "dry_run": "true" if doc.get("dry_run") is True else "false",
        })


def _full(pattern: re.Pattern[str], value: str) -> bool:
    return pattern.fullmatch(value) is not None


def validate_args(operation: str, args: Sequence[str]) -> list[str]:
    """Problems with an operation's arguments (empty when valid). Pure; mirrors ValidateSurgeryArgs."""
    problems: list[str] = []

    def address(i: int) -> None:
        a = args[i]
        if len(a) > MAX_ADDRESS or not _full(ADDRESS_RE, a):
            problems.append(f"args[{i}] must be a Terraform resource address such as module.queue.aws_sqs_queue.this")

    if operation == OP_STATE_RM:
        if not 1 <= len(args) <= 10:
            return ["state rm takes 1 to 10 resource addresses"]
        for i in range(len(args)):
            address(i)
    elif operation == OP_STATE_MV:
        if len(args) != 2:
            return ["state mv takes a source and a destination address"]
        address(0)
        address(1)
        if args[0] == args[1]:
            problems.append("args[1] must differ from the source address")
    elif operation == OP_IMPORT:
        if len(args) != 2:
            return ["import takes a resource address and the resource's ID"]
        address(0)
        if not _full(IMPORT_ID_RE, args[1]):
            problems.append("args[1] must be a resource ID or ARN (letters, digits and :/_.@=+,-)")
    elif operation == OP_FORCE_UNLOCK:
        if len(args) != 1:
            return ["force-unlock takes the lock ID"]
        if not _full(LOCK_ID_RE, args[0]):
            problems.append("args[0] must be the hexadecimal lock ID shown in the lock error")
    else:
        problems.append("operation must be one of: " + ", ".join(OPERATIONS))
    return problems


def parse_bool(value: str) -> bool | None:
    """A workflow boolean input ('true'/'false'), None when it is neither. Pure."""
    v = value.strip().lower()
    return {"true": True, "false": False}.get(v)


def parse_request(inputs: Mapping[str, str]) -> SurgeryRequest:
    """Validate raw dispatch inputs. Pure.

    ``args`` is whitespace-separated: addresses and ids never contain spaces, so no
    quoting rules exist that could be abused. Raises SurgeryInputError naming every bad input.
    """
    stack_path = inputs.get("stack_path", "").strip().rstrip("/")
    operation = inputs.get("operation", "").strip()
    raw_args = inputs.get("args", "")
    ticket = inputs.get("ticket", "").strip()
    requested_by = inputs.get("requested_by", "").strip()
    dry_run = parse_bool(inputs.get("dry_run", "true"))

    problems: list[str] = []
    if not _full(STACK_PATH_RE, stack_path):
        problems.append("stack_path must look like environments/<env>/<team>/<stack>")
    if len(raw_args) > MAX_ARGS_INPUT:
        problems.append(f"args must be at most {MAX_ARGS_INPUT} characters")
        args: list[str] = []
    else:
        args = raw_args.split()
    if operation not in OPERATIONS:
        problems.append("operation must be one of: " + ", ".join(OPERATIONS))
    else:
        problems.extend(validate_args(operation, args))
    if not _full(TICKET_RE, ticket):
        problems.append("ticket must be an incident or change ticket such as INC-1234")
    if not _full(LOGIN_RE, requested_by):
        problems.append("requested_by must be the GitHub login that dispatched the run")
    if dry_run is None:
        problems.append("dry_run must be true or false")
    if problems:
        raise SurgeryInputError(problems)
    return SurgeryRequest(stack_path=stack_path, operation=operation, args=tuple(args), ticket=ticket,
                          requested_by=requested_by, dry_run=bool(dry_run))


def terraform_argv(req: SurgeryRequest) -> list[str] | None:
    """The one Terraform command a request runs, or None for a dry run of an operation
    without a native -dry-run (it is validated and recorded, nothing runs). Pure.

    Arguments were validated not to start with '-', so they cannot become options.
    """
    if req.operation == OP_STATE_RM:
        return ["terraform", "state", "rm", *(["-dry-run"] if req.dry_run else []), *req.args]
    if req.operation == OP_STATE_MV:
        return ["terraform", "state", "mv", *(["-dry-run"] if req.dry_run else []), *req.args]
    if req.dry_run:
        return None
    if req.operation == OP_IMPORT:
        return ["terraform", "import", "-input=false", "-no-color", *req.args]
    if req.operation == OP_FORCE_UNLOCK:
        return ["terraform", "force-unlock", "-force", *req.args]
    raise ValueError("operation is not allow-listed")


# The post-step plan: read-only (no lock), exit code 0 = no changes, 2 = changes, 1 = error.
PLAN_ARGV = ["terraform", "plan", "-input=false", "-lock=false", "-no-color", "-detailed-exitcode"]


@dataclass(frozen=True)
class SurgeryResult:
    """What running a request did."""

    outcome: str
    exit_code: int | None = None
    plan_exit_code: int | None = None
    state_before_sha256: str = ""
    state_after_sha256: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {"outcome": self.outcome, "exit_code": self.exit_code, "plan_exit_code": self.plan_exit_code,
                "state_before_sha256": self.state_before_sha256, "state_after_sha256": self.state_after_sha256}

    @classmethod
    def from_dict(cls, doc: Mapping[str, Any]) -> SurgeryResult:
        def opt_int(v: Any) -> int | None:
            return v if isinstance(v, int) and not isinstance(v, bool) else None

        def digest(v: Any) -> str:
            return v if isinstance(v, str) and re.fullmatch(r"[0-9a-f]{64}", v) else ""

        outcome = doc.get("outcome")
        if outcome not in (OUTCOME_SUCCEEDED, OUTCOME_FAILED):
            outcome = OUTCOME_FAILED
        return cls(outcome=str(outcome), exit_code=opt_int(doc.get("exit_code")),
                   plan_exit_code=opt_int(doc.get("plan_exit_code")),
                   state_before_sha256=digest(doc.get("state_before_sha256")),
                   state_after_sha256=digest(doc.get("state_after_sha256")))


def outcome_for(exit_code: int | None) -> str:
    """succeeded for exit 0 or a dry run that ran nothing, else failed. Pure."""
    return OUTCOME_SUCCEEDED if exit_code in (0, None) else OUTCOME_FAILED


def _github(run: Mapping[str, Any]) -> dict[str, Any]:
    return {"run_id": run["run_id"], "run_attempt": run["run_attempt"], "head_sha": run["head_sha"]}


def build_report(req: SurgeryRequest, result: SurgeryResult, run: Mapping[str, Any]) -> dict[str, Any]:
    """The POST /v1/internal/state-surgery body for a request that ran. Pure."""
    body: dict[str, Any] = {**req.to_dict(), "outcome": result.outcome, "github": _github(run)}
    for key in ("exit_code", "plan_exit_code"):
        if getattr(result, key) is not None:
            body[key] = getattr(result, key)
    for key in ("state_before_sha256", "state_after_sha256"):
        if getattr(result, key):
            body[key] = getattr(result, key)
    return body


def refused_report(inputs: Mapping[str, str], run: Mapping[str, Any]) -> dict[str, Any]:
    """The report of a run whose inputs were refused: only values that pass validation are
    sent (the stack path, an allow-listed operation, a valid ticket and login); the rejected
    arguments never leave the job. Pure."""
    stack_path = inputs.get("stack_path", "").strip().rstrip("/")
    operation = inputs.get("operation", "").strip()
    ticket = inputs.get("ticket", "").strip()
    login = inputs.get("requested_by", "").strip()
    body: dict[str, Any] = {
        "stack_path": stack_path if _full(STACK_PATH_RE, stack_path) else "",
        "operation": operation if operation in OPERATIONS else "",
        "args": [],
        "ticket": ticket if _full(TICKET_RE, ticket) else "",
        "requested_by": login if _full(LOGIN_RE, login) else "unknown",
        "dry_run": parse_bool(inputs.get("dry_run", "true")) is not False,
        "outcome": OUTCOME_REFUSED,
        "github": _github(run),
    }
    return body


def state_digest(stack_dir: Path) -> str:
    """sha256 of the local state file, '' when there is none (local backend, ADR-0009)."""
    path = stack_dir / "terraform.tfstate"
    if not path.is_file():
        return ""
    return hashlib.sha256(path.read_bytes()).hexdigest()


@dataclass
class TerraformRunner:
    """Runs argv lists in a stack directory, never through a shell (adapter)."""

    stack_dir: Path
    calls: list[list[str]] = field(default_factory=list)

    def run(self, argv: Sequence[str]) -> int:
        self.calls.append(list(argv))
        return subprocess.run(list(argv), cwd=self.stack_dir, check=False).returncode


class SurgeryRunner:
    """Runs one validated request: digest, command, digest, post-step plan."""

    def __init__(self, runner: TerraformRunner) -> None:
        self._runner = runner

    def run(self, req: SurgeryRequest) -> SurgeryResult:
        stack_dir = self._runner.stack_dir
        before = state_digest(stack_dir)
        argv = terraform_argv(req)
        exit_code = self._runner.run(argv) if argv is not None else None
        after = state_digest(stack_dir)
        plan = self._runner.run(PLAN_ARGV) if exit_code in (0, None) else None
        return SurgeryResult(outcome=outcome_for(exit_code), exit_code=exit_code, plan_exit_code=plan,
                             state_before_sha256=before, state_after_sha256=after)


def error_excerpt(log: str, limit: int = 2_000) -> str:
    """The Terraform error of a failed run for the run report (the classifier reads it).

    From the first ``Error:`` line on, ANSI colour codes removed and lines that would read
    as workflow commands (``::``) neutralised, bounded to ``limit`` characters; the last 20 lines when no
    ``Error:`` line exists. Pure.
    """
    clean = re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", log)
    lines = [line.rstrip() for line in clean.splitlines()]
    start = next((i for i, line in enumerate(lines) if line.lstrip("│ ").startswith("Error:")), None)
    picked = lines[start:] if start is not None else lines[-20:]
    text = "\n".join(": :" + line.lstrip()[2:] if line.lstrip().startswith("::") else line for line in picked).strip()
    return text[:limit]
