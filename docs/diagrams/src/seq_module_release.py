"""Sequence: a module maintainer publishes a new module version."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from seqlib import Sequence  # noqa: E402

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "images",
                   "seq-module-release.png")


def build() -> Sequence:
    s = Sequence("Module release: modules/s3 v1.3.0",
                 subtitle="SemVer tags modules/<name>/vX.Y.Z; module.yaml is the catalog source of truth")
    s.participant("mnt", "Module\nmaintainer", "actor")
    s.participant("gh", "GitHub\nRepository", "github")
    s.participant("gha", "GitHub Actions\nmodule-release.yml", "github")
    s.participant("ls", "LocalStack /\nsandbox account", "aws")
    s.participant("api", "Foundry API\n(Catalog service)")
    s.participant("db", "PostgreSQL", "data")
    s.participant("portal", "Portal", "service")
    s.participant("dev", "Developers", "actor")

    s.msg("mnt", "gh", "PR: change modules/s3/** + bump module.yaml version")
    s.msg("gh", "gha", "PR checks for modules/s3")
    s.self_msg("gha", "fmt, validate, TFLint, Checkov, terraform-docs drift")
    s.self_msg("gha", "Validate module.yaml schema; SemVer bump vs. API diff")
    s.msg("gha", "ls", "Terratest: apply examples/, assert, destroy")
    s.ret("ls", "gha", "tests pass")
    s.frame("alt", "breaking change (inputs removed / renamed)")
    s.self_msg("gha", "Require major bump + UPGRADE notes, else fail", fail=True)
    s.end()
    s.msg("mnt", "gh", "CODEOWNERS approve; merge to main")
    s.msg("gh", "gha", "push to main -> release job")
    s.msg("gha", "gh", "Create tag modules/s3/v1.3.0 + GitHub release notes")
    s.msg("gha", "api", "POST /v1/internal/catalog/index (OIDC JWT, module, version, sha)")
    s.msg("api", "db", "INSERT module_versions (inputs schema, outputs, changelog)")
    s.msg("api", "db", "Flag deprecations; compute upgrade paths from older versions")
    s.msg("api", "db", "audit_event(module.released)")
    s.ret("api", "gha", "201 indexed")
    s.msg("portal", "api", "GET /v1/catalog/modules/s3")
    s.ret("api", "portal", "versions, changelog, stacks on older versions")
    s.msg("portal", "dev", "New version badge + 'Upgrade available' on stacks")
    s.note("dev", "Upgrade = new request bumping ?ref= (normal PR pipeline)",
           span=("portal", "dev"))
    return s


if __name__ == "__main__":
    build().render(OUT)
    print("wrote", os.path.normpath(OUT))
