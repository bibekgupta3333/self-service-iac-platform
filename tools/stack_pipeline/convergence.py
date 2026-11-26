"""Idempotence check for the LocalStack apply round trip.

After ``terraform apply`` a fresh plan must be empty: any change left over means the
stack does not converge. One difference is the emulator's, not the stack's: LocalStack
drops the tags passed to ``PutMetricAlarm``, so the provider keeps proposing to add them
(see ``smoke.EMULATOR_UNTAGGED``). A tag-only update of such a resource is not counted.
Everything here is pure; the caller reads the plan JSON.
"""

from __future__ import annotations

from typing import Any, Mapping

from .smoke import EMULATOR_UNTAGGED

TAG_ATTRIBUTES = frozenset({"tags", "tags_all"})
QUIET_ACTIONS = (["no-op"], ["read"])


def _tag_only_update(change: Mapping[str, Any]) -> bool:
    before, after = change.get("before"), change.get("after")
    if not isinstance(before, Mapping) or not isinstance(after, Mapping):
        return False
    keys = set(before) | set(after)
    return all(before.get(k) == after.get(k) for k in keys - TAG_ATTRIBUTES)


def residual_changes(plan: Mapping[str, Any], emulator_untagged: frozenset[str] = EMULATOR_UNTAGGED) -> tuple[str, ...]:
    """Resource changes in a re-plan that are not explained by the emulator. Pure."""
    residual: list[str] = []
    for rc in plan.get("resource_changes") or []:
        change = rc.get("change") or {}
        actions = list(change.get("actions") or [])
        if actions in QUIET_ACTIONS:
            continue
        if actions == ["update"] and rc.get("type") in emulator_untagged and _tag_only_update(change):
            continue
        residual.append(f"{rc.get('address', '?')}: {'+'.join(actions) or 'unknown'}")
    return tuple(residual)
