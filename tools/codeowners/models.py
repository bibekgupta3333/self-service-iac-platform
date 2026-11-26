"""Value types for the CODEOWNERS source of truth (``.github/teams.yaml``).

Parsing validates everything up front, so rendering never sees a bad slug or owner.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Any, Mapping

ENVIRONMENTS = ("development", "staging", "production")

# Same rule as teams.slug in backend/migrations/0001_init.sql.
_TEAM_RE = re.compile(r"[a-z][a-z0-9-]{1,38}")
_ORG_RE = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})")
# @user, @org/team, or an e-mail address (all forms CODEOWNERS accepts).
_OWNER_RE = re.compile(r"@[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:/[A-Za-z0-9._-]+)?|[^@\s]+@[^@\s]+\.[^@\s]+")
# Repo-anchored path patterns only: no spaces, no negation (CODEOWNERS has none).
_PATH_RE = re.compile(r"/[A-Za-z0-9._*/-]*")

DEFAULT_PLATFORM_PATHS = ("/modules/", "/policies/", "/.github/", "/.tflint.hcl", "/environments/")


class ConfigError(ValueError):
    """teams.yaml is malformed."""


def _owners(value: Any, where: str) -> tuple[str, ...]:
    if not isinstance(value, list) or not value:
        raise ConfigError(f"{where}: must be a non-empty list of owners")
    bad = [o for o in value if not isinstance(o, str) or not _OWNER_RE.fullmatch(o)]
    if bad:
        raise ConfigError(f"{where}: invalid owner(s) {bad!r}; use @user, @org/team or an e-mail")
    return tuple(value)


@dataclass(frozen=True)
class Team:
    """One tenant team and who owns its stack roots."""

    slug: str
    owners: tuple[str, ...] = ()
    environment_owners: Mapping[str, tuple[str, ...]] = field(default_factory=dict)

    def owners_for(self, environment: str, org: str | None) -> tuple[str, ...]:
        """Owners of ``environments/<environment>/<slug>/``. Pure."""
        if environment in self.environment_owners:
            return self.environment_owners[environment]
        if self.owners:
            return self.owners
        if org is None:
            raise ConfigError(f"team {self.slug!r} has no owners and no org is set to derive @<org>/{self.slug}")
        return (f"@{org}/{self.slug}",)

    @classmethod
    def from_yaml(cls, slug: Any, doc: Any) -> Team:
        if not isinstance(slug, str) or not _TEAM_RE.fullmatch(slug):
            raise ConfigError(f"team slug {slug!r} must match {_TEAM_RE.pattern}")
        doc = doc or {}
        if not isinstance(doc, dict):
            raise ConfigError(f"team {slug!r}: must be a mapping")
        unknown = set(doc) - {"owners", "environments"}
        if unknown:
            raise ConfigError(f"team {slug!r}: unknown keys {sorted(unknown)}")
        owners = _owners(doc["owners"], f"team {slug!r} owners") if "owners" in doc else ()
        envs = doc.get("environments") or {}
        if not isinstance(envs, dict):
            raise ConfigError(f"team {slug!r} environments: must be a mapping")
        bad_envs = sorted(set(envs) - set(ENVIRONMENTS))
        if bad_envs:
            raise ConfigError(f"team {slug!r}: unknown environment(s) {bad_envs}")
        env_owners = {e: _owners(v, f"team {slug!r} environments.{e}") for e, v in envs.items()}
        return cls(slug=slug, owners=owners, environment_owners=env_owners)


@dataclass(frozen=True)
class OwnersConfig:
    """The whole teams.yaml document."""

    org: str | None
    default_owners: tuple[str, ...]
    platform_owners: tuple[str, ...]
    platform_paths: tuple[str, ...]
    teams: tuple[Team, ...]

    def team(self, slug: str) -> Team | None:
        return next((t for t in self.teams if t.slug == slug), None)

    @classmethod
    def from_yaml(cls, doc: Any) -> OwnersConfig:
        if not isinstance(doc, dict):
            raise ConfigError("teams.yaml must be a mapping")
        unknown = set(doc) - {"org", "default_owners", "platform_owners", "platform_paths", "teams"}
        if unknown:
            raise ConfigError(f"unknown top-level keys {sorted(unknown)}")
        org = doc.get("org")
        if org is not None and (not isinstance(org, str) or not _ORG_RE.fullmatch(org)):
            raise ConfigError(f"org {org!r} is not a GitHub organisation name")
        paths = doc.get("platform_paths", list(DEFAULT_PLATFORM_PATHS))
        if not isinstance(paths, list) or not all(isinstance(p, str) and _PATH_RE.fullmatch(p) for p in paths):
            raise ConfigError("platform_paths must be a list of repo-anchored paths such as /modules/")
        shadowed = [p for p in paths if p.startswith("/environments/") and p != "/environments/"]
        if shadowed:
            raise ConfigError(
                f"platform_paths {shadowed} are under /environments/<env>/, where team rules come later and "
                "always win; set per-environment owners on the team instead (teams.<slug>.environments)"
            )
        teams_doc = doc.get("teams") or {}
        if not isinstance(teams_doc, dict):
            raise ConfigError("teams must be a mapping of slug to settings")
        teams = tuple(Team.from_yaml(slug, t) for slug, t in teams_doc.items())
        cfg = cls(
            org=org,
            default_owners=_owners(doc.get("default_owners"), "default_owners"),
            platform_owners=_owners(doc.get("platform_owners"), "platform_owners"),
            platform_paths=tuple(paths),
            teams=teams,
        )
        for t in teams:  # fail at load time, not half way through rendering
            for env in ENVIRONMENTS:
                t.owners_for(env, org)
        return cfg
