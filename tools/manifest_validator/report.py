"""Collects findings and renders them for humans or machines."""

from __future__ import annotations

import json
from collections import defaultdict

from .models import Finding, Severity


class Report:
    """Accumulates findings across modules."""

    def __init__(self) -> None:
        self.findings: list[Finding] = []
        self.modules: list[str] = []

    def add_module(self, name: str, findings: list[Finding]) -> None:
        self.modules.append(name)
        self.findings.extend(findings)

    @property
    def errors(self) -> list[Finding]:
        return [f for f in self.findings if f.severity is Severity.ERROR]

    @property
    def ok(self) -> bool:
        return not self.errors

    def by_module(self) -> dict[str, list[Finding]]:
        grouped: dict[str, list[Finding]] = defaultdict(list)
        for finding in self.findings:
            grouped[finding.module].append(finding)
        return grouped

    def render_text(self) -> str:
        grouped = self.by_module()
        lines = []
        for module in self.modules:
            items = grouped.get(module, [])
            lines.append(f"{'FAIL' if items else 'ok  '} {module}")
            lines.extend(f"     {f.render()}" for f in items)
        lines.append(f"{len(self.modules)} module(s) checked, {len(self.errors)} error(s)")
        return "\n".join(lines)

    def render_json(self) -> str:
        return json.dumps(
            {
                "ok": self.ok,
                "modules": self.modules,
                "findings": [
                    {"module": f.module, "code": f.code, "severity": f.severity.value, "message": f.message}
                    for f in self.findings
                ],
            },
            indent=2,
        )
