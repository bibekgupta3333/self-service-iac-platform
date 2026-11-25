"""Entity-relationship diagram of the Foundry PostgreSQL schema."""

from dotutil import render_dot

FONT = "Helvetica"

# group -> (header colour, tables)
GROUPS = {
    "Identity & access": ("#1F3A5F", {
        "teams": ["id uuid PK", "slug text UK", "github_team_id bigint", "created_at timestamptz"],
        "users": ["id uuid PK", "github_id bigint UK", "login text", "email text"],
        "memberships": ["team_id FK", "user_id FK", "role enum: viewer, developer,\nmaintainer, admin",
                        "PK (team_id, user_id)"],
        "budgets": ["id uuid PK", "team_id FK", "month date", "limit_usd numeric",
                    "spent_usd numeric", "override_by FK users"],
    }),
    "Catalog": ("#2E7D4F", {
        "modules": ["id uuid PK", "name text UK", "description", "owners text[]",
                    "deprecated bool"],
        "module_versions": ["id uuid PK", "module_id FK", "version semver", "git_ref text",
                            "inputs_schema jsonb", "outputs jsonb", "changelog text",
                            "tf_range,aws_provider_range text", "deprecated bool",
                            "UK (module_id, version)"],
    }),
    "Stacks": ("#B26B00", {
        "environments": ["id uuid PK", "name development, staging,\nproduction", "aws_account_id",
                         "requires_approval bool"],
        "stacks": ["id uuid PK", "team_id FK", "environment_id FK", "name text",
                   "status stack_status", "state_key text", "expires_at timestamptz",
                   "UK (team_id, environment_id, name)"],
        "stack_resources": ["id uuid PK", "stack_id FK", "module_version_id FK",
                            "name text", "inputs jsonb", "outputs jsonb",
                            "depends_on uuid[]"],
        "locks": ["stack_id PK FK", "request_id FK", "holder text", "acquired_at",
                  "expires_at"],
        "drift_reports": ["id uuid PK", "stack_id FK", "pipeline_run_id FK",
                          "status clean, drifted, error", "changed jsonb", "detected_at"],
    }),
    "Change requests & pipelines": ("#4B3F8F", {
        "requests": ["id uuid PK", "stack_id FK", "kind create, update, upgrade,\ndestroy, reconcile, rollback",
                     "status request_status", "created_by FK users", "pr_number int",
                     "branch text", "approved_plan_hash", "created_at,updated_at timestamptz"],
        "pipeline_runs": ["id uuid PK", "request_id FK", "workflow text",
                          "gh_run_id bigint", "commit_sha", "status", "started_at,finished_at timestamptz"],
        "plan_summaries": ["id uuid PK", "pipeline_run_id FK", "to_add,to_change,to_destroy int",
                           "plan_hash text", "policy_results jsonb"],
        "cost_estimates": ["id uuid PK", "pipeline_run_id FK", "monthly_total numeric",
                           "monthly_delta numeric", "currency"],
    }),
    "Audit": ("#5B6675", {
        "audit_events": ["id bigserial PK", "actor text", "action text",
                         "subject_type,subject_id text", "payload jsonb", "prev_hash bytea",
                         "hash bytea", "created_at", "append-only (no UPDATE/DELETE)"],
    }),
}

# (child, parent, label)
RELS = [
    ("memberships", "teams", ""), ("memberships", "users", ""),
    ("budgets", "teams", ""),
    ("module_versions", "modules", ""),
    ("stacks", "teams", "owns"), ("stacks", "environments", ""),
    ("stack_resources", "stacks", ""), ("stack_resources", "module_versions", "pins ?ref"),
    ("locks", "stacks", "1:1"), ("locks", "requests", "held by"),
    ("drift_reports", "stacks", ""),
    ("requests", "stacks", ""), ("requests", "users", "created_by"),
    ("pipeline_runs", "requests", ""),
    ("plan_summaries", "pipeline_runs", ""), ("cost_estimates", "pipeline_runs", ""),
]


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def table(name: str, cols: list[str], color: str) -> str:
    rows = []
    for c in cols:
        parts = c.split(" ", 1)
        col, typ = parts[0], (parts[1] if len(parts) > 1 else "")
        bold = "PK" in c or ("FK" in c and "PK" not in c)
        if c.startswith(("PK (", "UK (", "append-only")):
            rows.append(f'<TR><TD ALIGN="LEFT" COLSPAN="2"><FONT POINT-SIZE="10" '
                        f'COLOR="#5B6675"><I>{esc(c)}</I></FONT></TD></TR>')
            continue
        typ_html = esc(typ).replace("\n", '<BR ALIGN="LEFT"/>') or " "
        key_color = "#B42318" if "PK" in typ else ("#1F3A5F" if "FK" in typ else "#1F2933")
        col_html = f"<B>{esc(col)}</B>" if bold else esc(col)
        rows.append(f'<TR><TD ALIGN="LEFT"><FONT COLOR="{key_color}">{col_html}</FONT></TD>'
                    f'<TD ALIGN="LEFT"><FONT COLOR="#5B6675">{typ_html}</FONT></TD></TR>')
    return (f'  {name} [label=<<TABLE BORDER="1" CELLBORDER="0" CELLSPACING="0" '
            f'CELLPADDING="4" COLOR="{color}" BGCOLOR="white">'
            f'<TR><TD COLSPAN="2" BGCOLOR="{color}"><FONT COLOR="white"><B>{name}</B></FONT>'
            f'</TD></TR>{"".join(rows)}</TABLE>>];')


def build() -> str:
    lines = [
        "digraph ERD {",
        '  graph [rankdir=LR, fontname="%s", fontsize=22, labelloc=t, pad=0.4, '
        'nodesep=0.45, ranksep=0.9, splines=spline, bgcolor=white, '
        'label="Foundry: PostgreSQL data model (key columns)\\n\\n"];' % FONT,
        f'  node [shape=plaintext, fontname="{FONT}", fontsize=11];',
        f'  edge [fontname="{FONT}", fontsize=10, color="#5B6675", dir=both, '
        'penwidth=1.2];',
    ]
    for i, (group, (color, tables)) in enumerate(GROUPS.items()):
        lines.append(f'  subgraph cluster_{i} {{ label="{group}"; fontsize=14; '
                     f'labeljust=l; style="rounded,dashed"; color="#9AA5B1"; margin=14;')
        for name, cols in tables.items():
            lines.append(table(name, cols, color))
        lines.append("  }")
    for child, parent, label in RELS:
        # parent (one) on the left, child (many) on the right
        lab = f', label="{label}"' if label else ""
        lines.append(f"  {parent} -> {child} [arrowtail=tee, arrowhead=crow{lab}];")
    lines.append("}")
    return "\n".join(lines)


if __name__ == "__main__":
    print("wrote", render_dot(build(), "data-model"))
