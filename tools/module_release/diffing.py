"""Pure interface diff and SemVer classification.

Rules follow docs/04-module-registry.md section 5.2, from the consumer's
point of view:

* MAJOR: variable or output removed (a rename is a removal plus an addition),
  new required variable, type changed, default removed, ``nullable`` turned
  off, validation condition added or changed (it may reject values that were
  valid before), output marked ``sensitive``.
* MINOR: new optional variable, new output, default added or changed,
  ``nullable`` turned on, validation condition removed, output no longer
  ``sensitive``.
* Terraform and provider constraints: raising the lowest allowed major
  version is MAJOR, raising the lowest allowed minor version is MINOR, and a
  new required provider is MINOR and a removed one (or a changed source) is
  MAJOR (see ``constraint_floor``).
* PATCH: any other change to release-relevant files.
"""

from __future__ import annotations

import re
from typing import Iterable

from .models import Bump, Change, Interface, Subject, Variable

_CONSTRAINT = re.compile(r"^(>=|<=|!=|~>|>|<|=)?\s*v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:[-+][0-9A-Za-z.-]*)?$")
_LOWER_BOUND_OPS = frozenset({"", "=", ">=", ">", "~>"})


def _variable_changes(old: Variable, new: Variable) -> list[Change]:
    name = new.name
    changes: list[Change] = []

    def add(description: str, bump: Bump) -> None:
        changes.append(Change(Subject.VARIABLE, name, description, bump))

    if old.type != new.type:
        add(f"type changed from '{old.type}' to '{new.type}'", Bump.MAJOR)
    if not old.required and new.required:
        add("default removed: the variable is now required", Bump.MAJOR)
    elif old.required and not new.required:
        add(f"default added ({new.default!r}): the variable is now optional", Bump.MINOR)
    elif not new.required and old.default != new.default:
        add(f"default changed from {old.default!r} to {new.default!r}", Bump.MINOR)
    if old.nullable and not new.nullable:
        add("nullable turned off: null is no longer accepted", Bump.MAJOR)
    elif not old.nullable and new.nullable:
        add("nullable turned on", Bump.MINOR)
    added = sorted(set(new.validations) - set(old.validations))
    removed = sorted(set(old.validations) - set(new.validations))
    if added:
        add(f"validation added or changed ({len(added)} condition(s)): may reject values valid before", Bump.MAJOR)
    elif removed:
        add(f"validation removed ({len(removed)} condition(s))", Bump.MINOR)
    return changes


def diff_interfaces(old: Interface, new: Interface) -> list[Change]:
    """Every consumer-visible difference between two interfaces, in a stable order."""
    changes: list[Change] = []
    for name in sorted(set(old.variables) | set(new.variables)):
        before, after = old.variables.get(name), new.variables.get(name)
        if after is None:
            changes.append(Change(Subject.VARIABLE, name, "removed (or renamed)", Bump.MAJOR))
        elif before is None:
            if after.required:
                changes.append(Change(Subject.VARIABLE, name, "added as required (no default)", Bump.MAJOR))
            else:
                changes.append(Change(Subject.VARIABLE, name, f"added as optional (default {after.default!r})", Bump.MINOR))
        else:
            changes.extend(_variable_changes(before, after))
    for name in sorted(set(old.outputs) - set(new.outputs)):
        changes.append(Change(Subject.OUTPUT, name, "removed (or renamed)", Bump.MAJOR))
    for name in sorted(set(new.outputs) - set(old.outputs)):
        changes.append(Change(Subject.OUTPUT, name, "added", Bump.MINOR))
    for name in sorted(set(old.outputs) & set(new.outputs)):
        was, now = old.outputs[name].sensitive, new.outputs[name].sensitive
        if not was and now:
            changes.append(Change(Subject.OUTPUT, name,
                                  "marked sensitive: consumers that re-export it must mark theirs sensitive", Bump.MAJOR))
        elif was and not now:
            changes.append(Change(Subject.OUTPUT, name, "no longer sensitive", Bump.MINOR))
    changes.extend(diff_requirements(old, new))
    return changes


def constraint_floor(constraint: str | None) -> tuple[int, int, int] | None:
    """Lowest version a Terraform version constraint allows, or None if it cannot be read.

    The floor is the highest lower bound among the comma-separated parts
    (``>=``, ``>``, ``=``, ``~>`` or a bare version); ``<``, ``<=`` and ``!=``
    do not raise it. ``> X.Y.Z`` is treated as ``>= X.Y.Z``. No constraint, or
    no lower bound, means 0.0.0.
    """
    if constraint is None or not constraint.strip():
        return (0, 0, 0)
    floor = (0, 0, 0)
    for part in constraint.split(","):
        match = _CONSTRAINT.match(part.strip())
        if not match:
            return None
        op, major, minor, patch = match.groups()
        if (op or "") in _LOWER_BOUND_OPS:
            floor = max(floor, (int(major), int(minor or 0), int(patch or 0)))
    return floor


def _floor_change(subject: Subject, name: str, old: str | None, new: str | None) -> list[Change]:
    """Changes for one version constraint: a raised minimum major is MAJOR, a raised minor is MINOR."""
    if old == new:
        return []
    before, after = constraint_floor(old), constraint_floor(new)
    if before is None or after is None:
        return [Change(subject, name, f"version constraint changed from {old!r} to {new!r} "
                                      "and could not be compared", Bump.MAJOR)]
    label = ".".join(map(str, after))
    if after[0] > before[0]:
        return [Change(subject, name, f"minimum major version raised to {label} ({new!r})", Bump.MAJOR)]
    if after[:2] > before[:2]:
        return [Change(subject, name, f"minimum minor version raised to {label} ({new!r})", Bump.MINOR)]
    return []


def diff_requirements(old: Interface, new: Interface) -> list[Change]:
    """Changes to ``required_version`` and ``required_providers`` constraints."""
    changes = _floor_change(Subject.TERRAFORM, "required_version", old.terraform_version, new.terraform_version)
    for name in sorted(set(old.providers) - set(new.providers)):
        changes.append(Change(Subject.PROVIDER, name, "required provider removed (or its source changed)", Bump.MAJOR))
    for name in sorted(set(new.providers) - set(old.providers)):
        changes.append(Change(Subject.PROVIDER, name, f"new required provider ({new.providers[name]!r})", Bump.MINOR))
    for name in sorted(set(old.providers) & set(new.providers)):
        changes.extend(_floor_change(Subject.PROVIDER, name, old.providers[name], new.providers[name]))
    return changes


def required_bump(changes: Iterable[Change], content_changed: bool) -> Bump:
    """The smallest bump that covers ``changes``; PATCH if only non-interface files changed."""
    interface_bump = max((c.bump for c in changes), default=Bump.NONE)
    if interface_bump is Bump.NONE and content_changed:
        return Bump.PATCH
    return interface_bump


def is_release_relevant(relative_path: str) -> bool:
    """True if a path inside a module directory affects consumers.

    ``tests/`` and ``examples/`` are not used by consumers of the module source,
    so changing only those does not require a new version.
    """
    first = relative_path.strip("/").split("/", 1)[0]
    return first not in {"tests", "examples"}
