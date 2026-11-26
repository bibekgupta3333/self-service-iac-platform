"""Pure interface diff and SemVer classification.

Rules follow docs/04-module-registry.md section 5.2, from the consumer's
point of view:

* MAJOR: variable or output removed (a rename is a removal plus an addition),
  new required variable, type changed, default removed, ``nullable`` turned
  off, validation condition added or changed (it may reject values that were
  valid before).
* MINOR: new optional variable, new output, default added or changed,
  ``nullable`` turned on, validation condition removed.
* PATCH: any other change to release-relevant files.
"""

from __future__ import annotations

from typing import Iterable

from .models import Bump, Change, Interface, Subject, Variable


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
