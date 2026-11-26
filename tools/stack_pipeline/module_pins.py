"""Verify the module pins in iacp.stack.json and serve unpublished release tags locally.

Stacks source modules as ``git::<repo>.git//modules/<name>?ref=modules/<name>/vX.Y.Z``
and record the commit each tag resolved to. Before ``terraform init`` this step:

* **published tag** - checks that the tag on ``origin`` still resolves to the
  recorded commit (the integrity check Checkov's CKV_TF_1 asks for, done
  against the recorded SHA instead of in the source string);
* **unpublished tag** - refused unless the caller opts in with
  ``allow_unpublished`` (``--allow-unpublished``; the workflow passes it only
  while ``IACP_PIN_BOOTSTRAP`` is ``true`` under ADR-0009). When allowed, it checks that the recorded commit is on the trusted branch (``origin/main``)
  and that its ``modules/<name>/module.yaml`` declares exactly that version,
  then pushes that one commit as the tag into a throwaway bare mirror and
  routes the repository URL to it with ``url.<mirror>.insteadOf``.

The source string in the stack and in the plan stays the generator's pinned
form, so ``iacp.forbidden.module_source`` is evaluated unchanged. Code from the
PR itself is never served: the recorded commit must already be on main.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from enum import Enum
from pathlib import Path
from typing import Iterable, Mapping, Protocol, Sequence

import yaml

from .models import ModulePin, StackMetadata

DEFAULT_REPO_URL = "https://github.com/bibekgupta3333/self-service-iac-platform.git"
DEFAULT_TRUSTED_REF = "origin/main"

_TAG_RE = re.compile(r"^modules/([a-z0-9-]+)/v(\d+\.\d+\.\d+)$")
_SHA_RE = re.compile(r"^[0-9a-f]{40}$")


class PinError(Exception):
    """A module pin cannot be trusted."""


class Resolution(str, Enum):
    PUBLISHED = "published"
    BOOTSTRAPPED = "bootstrapped"


@dataclass(frozen=True)
class PinCheck:
    """Outcome for one module pin."""

    stack: str
    instance: str
    pin: ModulePin
    resolution: Resolution

    def render(self) -> str:
        return f"{self.stack}: module.{self.instance} {self.pin.ref} @ {self.pin.sha[:12]} ({self.resolution.value})"


# ---- pure functions ---------------------------------------------------------


def parse_tag(ref: str) -> tuple[str, str]:
    """``modules/s3/v1.0.0`` -> ("s3", "1.0.0")."""
    match = _TAG_RE.match(ref)
    if not match:
        raise PinError(f"{ref!r} is not a module release tag (modules/<name>/vX.Y.Z)")
    return match.group(1), match.group(2)


def manifest_version(manifest_text: str) -> str | None:
    """metadata.version from a module.yaml document, or None."""
    try:
        data = yaml.safe_load(manifest_text)
    except yaml.YAMLError:
        return None
    version = (data or {}).get("metadata", {}).get("version") if isinstance(data, Mapping) else None
    return str(version) if version is not None else None


def unique_pins(stacks: Iterable[StackMetadata]) -> list[tuple[str, str, ModulePin]]:
    """(stack key, instance, pin) for every module of every stack, in stable order."""
    return [(s.ref.key, name, s.modules[name]) for s in stacks for name in sorted(s.modules)]


def insteadof_config(mirror: Path, repo_url: str) -> dict[str, str]:
    """GIT_CONFIG_* variables routing repo_url (with and without .git) to mirror.

    Terraform fetches ``<repo>.git``; Checkov strips the ``.git`` suffix, so both
    spellings are rewritten. The variables apply only to processes that inherit
    them; no git config file is modified.
    """
    target = f"url.{mirror.resolve().as_uri()}.insteadOf"
    urls = sorted({repo_url, repo_url.removesuffix(".git")})
    env = {"GIT_CONFIG_COUNT": str(len(urls))}
    for i, url in enumerate(urls):
        env[f"GIT_CONFIG_KEY_{i}"] = target
        env[f"GIT_CONFIG_VALUE_{i}"] = url
    return env


# ---- adapter boundary -----------------------------------------------------------


class GitPort(Protocol):
    def remote_tag_commit(self, tag: str) -> str | None: ...
    def is_commit(self, sha: str) -> bool: ...
    def is_ancestor(self, sha: str, ref: str) -> bool: ...
    def show(self, sha: str, path: str) -> str | None: ...
    def publish_tag(self, mirror: Path, sha: str, tag: str) -> None: ...


class ModulePinVerifier:
    """Checks every pin, bootstrapping unpublished tags into a local mirror."""

    def __init__(
        self, git: GitPort, mirror: Path, trusted_ref: str = DEFAULT_TRUSTED_REF, allow_unpublished: bool = False
    ) -> None:
        self.git = git
        self.mirror = Path(mirror)
        self.trusted_ref = trusted_ref
        self.allow_unpublished = allow_unpublished

    def check(self, stack: str, instance: str, pin: ModulePin) -> PinCheck:
        name, version = parse_tag(pin.ref)
        where = f"{stack}: module.{instance} ({pin.ref})"
        if not _SHA_RE.match(pin.sha):
            raise PinError(f"{where}: recorded sha {pin.sha!r} is not a 40-character commit")
        published = self.git.remote_tag_commit(pin.ref)
        if published is not None:
            if published != pin.sha:
                raise PinError(f"{where}: tag resolves to {published} but iacp.stack.json records {pin.sha}")
            return PinCheck(stack, instance, pin, Resolution.PUBLISHED)
        if not self.allow_unpublished:
            raise PinError(
                f"{where}: tag is not published on the remote; unpublished tags are only served with "
                "--allow-unpublished (IACP_PIN_BOOTSTRAP, ADR-0009)"
            )
        if not self.git.is_commit(pin.sha):
            raise PinError(f"{where}: commit {pin.sha} is not in this clone (fetch full history)")
        if not self.git.is_ancestor(pin.sha, self.trusted_ref):
            raise PinError(f"{where}: unpublished tag and commit {pin.sha} is not on {self.trusted_ref}")
        declared = manifest_version(self.git.show(pin.sha, f"modules/{name}/module.yaml") or "")
        if declared != version:
            raise PinError(f"{where}: modules/{name}/module.yaml at {pin.sha[:12]} declares version {declared}, not {version}")
        self.git.publish_tag(self.mirror, pin.sha, pin.ref)
        return PinCheck(stack, instance, pin, Resolution.BOOTSTRAPPED)

    def verify(self, stacks: Sequence[StackMetadata]) -> list[PinCheck]:
        return [self.check(stack, instance, pin) for stack, instance, pin in unique_pins(stacks)]
