"""Post-apply smoke test for one stack (WBS 5.7).

After ``terraform apply`` the stack must have:

1. every output the configuration declares, with a non-null value;
2. the required ``iacp:*`` tags on every tagged resource in state (``tags_all``);
3. the same tags on the live resource, read back from the cloud API, for the
   resource types in ``PROBES``. A probe that cannot read the resource (it does
   not exist, or the call fails) fails the check.

The expectations and the probe table are pure. ``SmokeTester`` takes a
``TagReader`` adapter (``adapters.AwsCliTagReader``: the AWS CLI against the
LocalStack endpoint while ADR-0009 is in force), so the logic tests without AWS.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Iterator, Mapping, Protocol, Sequence
from urllib.parse import urlsplit

from .models import StackMetadata

MANAGED_BY = "iacp"
REQUIRED_TAG_KEYS = ("iacp:team", "iacp:environment", "iacp:stack", "iacp:managed-by")


class SmokeProbeError(RuntimeError):
    """A probe could not read the resource (missing resource, API error)."""


class SmokeConfigError(ValueError):
    """The smoke test is not configured to run against LocalStack."""


LOCAL_HOSTS = frozenset({"localhost", "127.0.0.1", "::1"})
LOCALSTACK_DOMAIN = ".localstack.cloud"


def local_endpoint(url: str) -> str:
    """``url`` when it is a LocalStack or loopback http(s) endpoint, else SmokeConfigError. Pure.

    While ADR-0009 is in force the probes must never reach real AWS: no endpoint
    (the AWS default) and any other host are refused, not silently allowed.
    """
    parts = urlsplit(url.strip())
    host = (parts.hostname or "").lower()
    if parts.scheme not in ("http", "https") or not host:
        raise SmokeConfigError(
            f"the smoke test needs a LocalStack endpoint (--endpoint-url or $AWS_ENDPOINT_URL), got {url!r}"
        )
    if host not in LOCAL_HOSTS and not host.endswith(LOCALSTACK_DOMAIN):
        raise SmokeConfigError(
            f"endpoint host {host!r} is not loopback or *{LOCALSTACK_DOMAIN}; the smoke test never calls real AWS (ADR-0009)"
        )
    return url.strip()


class TagReader(Protocol):
    """Runs one read-only cloud API call and returns its JSON response."""

    def read(self, argv: Sequence[str]) -> Mapping[str, Any]: ...


# ---- expectations ------------------------------------------------------------------


def required_tags(meta: StackMetadata) -> dict[str, str]:
    """The tags docs/07 requires on every resource of this stack. Pure."""
    return {
        "iacp:team": meta.ref.team,
        "iacp:environment": meta.ref.environment,
        "iacp:stack": meta.ref.stack,
        "iacp:managed-by": MANAGED_BY,
    }


def tag_problems(actual: Mapping[str, Any], expected: Mapping[str, str]) -> tuple[str, ...]:
    """What is missing or wrong in ``actual``, one message per tag. Pure."""
    return tuple(
        f"{key} is missing" if key not in actual else f"{key}={actual[key]!r}, expected {value!r}"
        for key, value in expected.items()
        if actual.get(key) != value
    )


@dataclass(frozen=True)
class AppliedResource:
    address: str
    type: str
    values: Mapping[str, Any]

    @property
    def tagged(self) -> bool:
        """True when the resource type supports tags (state carries ``tags_all``)."""
        return "tags_all" in self.values

    @property
    def state_tags(self) -> Mapping[str, Any]:
        tags = self.values.get("tags_all")
        return tags if isinstance(tags, Mapping) else {}


def _modules(module: Mapping[str, Any]) -> Iterator[Mapping[str, Any]]:
    yield module
    for child in module.get("child_modules") or []:
        if isinstance(child, Mapping):
            yield from _modules(child)


def applied_resources(state: Mapping[str, Any]) -> tuple[AppliedResource, ...]:
    """Managed resources from ``terraform show -json`` (state), sorted by address. Pure."""
    root = (state.get("values") or {}).get("root_module") or {}
    found = (
        AppliedResource(str(r.get("address", "")), str(r.get("type", "")), r.get("values") or {})
        for module in _modules(root)
        for r in module.get("resources") or []
        if isinstance(r, Mapping) and r.get("mode", "managed") == "managed"
    )
    return tuple(sorted(found, key=lambda r: r.address))


def declared_outputs(plan: Mapping[str, Any]) -> tuple[str, ...]:
    """Root output names from the configuration in ``terraform show -json tfplan``. Pure."""
    outputs = ((plan.get("configuration") or {}).get("root_module") or {}).get("outputs") or {}
    return tuple(sorted(outputs))


# ---- probes: how to read a live resource's tags ---------------------------------------


def _pairs(field_name: str, key: str = "Key", value: str = "Value") -> Callable[[Mapping[str, Any]], dict[str, Any]]:
    def parse(resp: Mapping[str, Any]) -> dict[str, Any]:
        return {t[key]: t.get(value) for t in resp.get(field_name) or [] if isinstance(t, Mapping) and key in t}

    return parse


def _mapping(field_name: str) -> Callable[[Mapping[str, Any]], dict[str, Any]]:
    def parse(resp: Mapping[str, Any]) -> dict[str, Any]:
        tags = resp.get(field_name)
        return dict(tags) if isinstance(tags, Mapping) else {}

    return parse


@dataclass(frozen=True)
class TagProbe:
    """A read-only AWS CLI call that returns a resource's tags."""

    command: tuple[str, ...]  # service and operation, e.g. ("s3api", "get-bucket-tagging")
    option: str  # the identifying option, e.g. "--bucket"
    attributes: tuple[str, ...]  # state attributes to identify the resource, first non-empty wins
    parse: Callable[[Mapping[str, Any]], dict[str, Any]]

    def argv(self, values: Mapping[str, Any]) -> list[str] | None:
        """The CLI arguments for this resource, or None when state has no identifier. Pure."""
        ident = next((values[a] for a in self.attributes if isinstance(values.get(a), str) and values[a]), None)
        return None if ident is None else [*self.command, self.option, ident]


PROBES: Mapping[str, TagProbe] = {
    "aws_s3_bucket": TagProbe(("s3api", "get-bucket-tagging"), "--bucket", ("bucket", "id"), _pairs("TagSet")),
    "aws_kms_key": TagProbe(("kms", "list-resource-tags"), "--key-id", ("key_id", "arn"),
                            _pairs("Tags", "TagKey", "TagValue")),
    "aws_sqs_queue": TagProbe(("sqs", "list-queue-tags"), "--queue-url", ("url", "id"), _mapping("Tags")),
    "aws_lambda_function": TagProbe(("lambda", "list-tags"), "--resource", ("arn",), _mapping("Tags")),
    "aws_iam_role": TagProbe(("iam", "list-role-tags"), "--role-name", ("name", "id"), _pairs("Tags")),
    "aws_cloudwatch_log_group": TagProbe(("logs", "list-tags-log-group"), "--log-group-name", ("name", "id"),
                                         _mapping("tags")),
    "aws_cloudwatch_metric_alarm": TagProbe(("cloudwatch", "list-tags-for-resource"), "--resource-arn", ("arn",),
                                            _pairs("Tags")),
    "aws_sns_topic": TagProbe(("sns", "list-tags-for-resource"), "--resource-arn", ("arn", "id"), _pairs("Tags")),
    "aws_secretsmanager_secret": TagProbe(("secretsmanager", "describe-secret"), "--secret-id", ("arn", "id"),
                                          _pairs("Tags")),
    "aws_cloudwatch_event_rule": TagProbe(("events", "list-tags-for-resource"), "--resource-arn", ("arn",),
                                          _pairs("Tags")),
}


# ---- result ------------------------------------------------------------------------


@dataclass(frozen=True)
class Check:
    kind: str  # output | state-tags | live-tags
    subject: str
    passed: bool
    detail: str = ""


@dataclass(frozen=True)
class SmokeResult:
    stack_path: str
    checks: tuple[Check, ...] = ()
    unprobed: tuple[str, ...] = field(default=())  # tagged types without a live probe

    @property
    def ok(self) -> bool:
        """No failed check. A stack with nothing to check passes (see ``empty``)."""
        return all(c.passed for c in self.checks)

    @property
    def empty(self) -> bool:
        return not self.checks

    @property
    def failures(self) -> tuple[Check, ...]:
        return tuple(c for c in self.checks if not c.passed)

    def to_dict(self) -> dict[str, Any]:
        return {
            "stack_path": self.stack_path,
            "ok": self.ok,
            "empty": self.empty,
            "checks": [{"kind": c.kind, "subject": c.subject, "passed": c.passed, "detail": c.detail} for c in self.checks],
            "unprobed_types": list(self.unprobed),
        }

    def _verdict(self) -> str:
        if self.empty:
            return "passed, nothing to check"
        return "passed" if self.ok else "failed"

    def render_markdown(self) -> str:
        counts = {kind: sum(1 for c in self.checks if c.kind == kind) for kind in ("output", "state-tags", "live-tags")}
        lines = [
            f"### Smoke test of `{self.stack_path}` ({self._verdict()})",
            "",
            f"{counts['output']} output(s), {counts['state-tags']} tagged resource(s) in state, "
            f"{counts['live-tags']} read back from the API.",
        ]
        if not self.checks:
            lines += ["", "Nothing to check: the stack declares no outputs and has no tagged resources."]
        if self.failures:
            lines += ["", *(f"- {c.kind} `{c.subject}`: {c.detail}" for c in self.failures)]
        if self.unprobed:
            lines += ["", f"Tags checked in state only (no live probe): {', '.join(f'`{t}`' for t in self.unprobed)}."]
        return "\n".join(lines) + "\n"


def output_checks(declared: Sequence[str], outputs: Mapping[str, Any]) -> tuple[Check, ...]:
    """Each declared output must exist in ``terraform output -json`` with a non-null value. Pure."""

    def check(name: str) -> Check:
        entry = outputs.get(name)
        if not isinstance(entry, Mapping) or "value" not in entry:
            return Check("output", name, False, "missing from terraform output")
        if entry["value"] is None:
            return Check("output", name, False, "value is null")
        return Check("output", name, True)

    return tuple(check(name) for name in declared)


def state_tag_checks(resources: Sequence[AppliedResource], expected: Mapping[str, str]) -> tuple[Check, ...]:
    """Required tags in ``tags_all`` of every tagged resource in state. Pure."""
    return tuple(
        Check("state-tags", r.address, not problems, "; ".join(problems))
        for r in resources
        if r.tagged
        for problems in (tag_problems(r.state_tags, expected),)
    )


class SmokeTester:
    """Runs the smoke checks for one stack; the only I/O is through the TagReader."""

    def __init__(self, reader: TagReader, probes: Mapping[str, TagProbe] = PROBES) -> None:
        self._reader = reader
        self._probes = probes

    def run(
        self, meta: StackMetadata, plan: Mapping[str, Any], outputs: Mapping[str, Any], state: Mapping[str, Any]
    ) -> SmokeResult:
        expected = required_tags(meta)
        resources = applied_resources(state)
        tagged = [r for r in resources if r.tagged]
        return SmokeResult(
            stack_path=meta.ref.path,
            checks=(
                *output_checks(declared_outputs(plan), outputs),
                *state_tag_checks(resources, expected),
                *(self._live(r, expected) for r in tagged if r.type in self._probes),
            ),
            unprobed=tuple(sorted({r.type for r in tagged if r.type not in self._probes})),
        )

    def _live(self, resource: AppliedResource, expected: Mapping[str, str]) -> Check:
        probe = self._probes[resource.type]
        argv = probe.argv(resource.values)
        if argv is None:
            return Check("live-tags", resource.address, False, f"no {'/'.join(probe.attributes)} in state")
        try:
            tags = probe.parse(self._reader.read(argv))
        except SmokeProbeError as exc:
            return Check("live-tags", resource.address, False, f"could not read the resource: {exc}")
        problems = tag_problems(tags, expected)
        return Check("live-tags", resource.address, not problems, "; ".join(problems))
