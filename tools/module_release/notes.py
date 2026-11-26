"""Pure release-note helpers: CHANGELOG section extraction and a diff summary fallback."""

from __future__ import annotations

import re

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
