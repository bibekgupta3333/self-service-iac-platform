"""Pure release-note helpers: CHANGELOG section extraction and a diff summary fallback."""

from __future__ import annotations

import re
from typing import Any, Mapping

from .models import SemVer
from .versioning import GateResult

_HEADING = re.compile(r"^##\s+\[?v?(?P<version>[0-9][^\]\s]*)\]?.*$", re.MULTILINE)


def changelog_section(changelog: str | None, version: SemVer) -> str | None:
    """Body of the ``## [X.Y.Z]`` section of a Keep a Changelog file, or None.

    The heading line itself is dropped; the body runs to the next ``## `` heading.
    """
    if not changelog:
        return None
    headings = list(_HEADING.finditer(changelog))
    for index, match in enumerate(headings):
        if SemVer.try_parse(match.group("version")) == version:
            end = headings[index + 1].start() if index + 1 < len(headings) else len(changelog)
            body = changelog[match.end():end].strip()
            return body or None
    return None


# "### Migration" (docs/04 section 5) or "### Upgrade notes", in any letter case; the
# subsection runs to the next heading of level 1 to 3. Mirrors catalog.MigrationNotes.
_MIGRATION = re.compile(r"^###[ \t]+(?:migration|upgrade notes?)\b.*$", re.IGNORECASE | re.MULTILINE)
_SUBSECTION_END = re.compile(r"^#{1,3}[ \t]", re.MULTILINE)


def migration_notes(section: str | None) -> str | None:
    """Body of the ``### Migration`` (or ``### Upgrade notes``) subsection of a CHANGELOG
    section, without its heading, or None when there is none."""
    if not section:
        return None
    match = _MIGRATION.search(section)
    if not match:
        return None
    body = section[match.end():]
    end = _SUBSECTION_END.search(body)
    if end:
        body = body[: end.start()]
    return body.strip() or None


def upgrade_notes(section: str | None, manifest: Mapping[str, Any]) -> str | None:
    """Upgrade notes for the catalog: the CHANGELOG section's migration subsection, else
    ``upgrade.notes`` from module.yaml, else None. Pure."""
    upgrade = manifest.get("upgrade")
    fallback = upgrade.get("notes") if isinstance(upgrade, Mapping) else None
    notes = migration_notes(section) or (str(fallback).strip() if fallback else "")
    return notes or None


def diff_summary(result: GateResult) -> str:
    """Markdown summary of a gate result, used when the CHANGELOG has no section."""
    lines = [f"Release of `{result.module}` {result.declared}."]
    if result.first_release:
        lines.append("")
        lines.append("First release of this module.")
        return "\n".join(lines)
    lines.append("")
    lines.append(f"Previous release: {result.previous}. Bump: {result.actual.label} (required: {result.required.label}).")
    if result.changes:
        lines.append("")
        lines.append("### Interface changes")
        lines.extend(f"- {c.render()}" for c in result.changes)
    else:
        lines.append("")
        lines.append("No interface changes.")
    return "\n".join(lines)


def release_notes(changelog: str | None, result: GateResult) -> str:
    """CHANGELOG section for the declared version, or the generated summary."""
    return changelog_section(changelog, result.declared) or diff_summary(result)
