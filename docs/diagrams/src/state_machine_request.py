"""State machines for change requests and stacks."""

from dotutil import render_dot

FONT = "Helvetica"

NORMAL = ('fillcolor="#E6EEF9", color="#1F3A5F"')
GOOD = ('fillcolor="#E7F4EC", color="#2E7D4F"')
BAD = ('fillcolor="#FDECEA", color="#B42318"')
WARN = ('fillcolor="#FFF4E5", color="#B26B00"')
TERM = ('fillcolor="#F1F2F4", color="#5B6675"')


def node(name, label, style, final=False):
    periph = ", peripheries=2" if final else ""
    return f'    {name} [label="{label}", {style}{periph}];'


def build() -> str:
    L = [
        "digraph States {",
        f'  graph [rankdir=LR, fontname="{FONT}", fontsize=22, labelloc=t, pad=0.4, '
        'nodesep=0.5, ranksep=0.75, bgcolor=white, newrank=true, '
        'label="Self-Service IaC Platform: request and stack state machines\\n\\n"];',
        f'  node [shape=box, style="rounded,filled", fontname="{FONT}", fontsize=13, '
        'penwidth=1.5, margin="0.2,0.1", height=0.5];',
        f'  edge [fontname="{FONT}", fontsize=10.5, color="#4A5568", penwidth=1.2, '
        'fontcolor="#1F2933"];',
        # ---------------- stack
        '  subgraph cluster_stack { label="Stack (stacks.status)"; fontsize=15; '
        'labeljust=l; style="rounded"; color="#9AA5B1"; bgcolor="#FFFDF8"; margin=18;',
        '    s_start [shape=circle, label="", width=0.22, style=filled, fillcolor="#1F2933", color="#1F2933"];',
        node("provisioning", "provisioning", WARN),
        node("active", "active", GOOD),
        node("drifted", "drifted", WARN),
        node("updating", "updating", WARN),
        node("destroying", "destroying", WARN),
        node("destroyed", "destroyed", TERM, True),
        node("s_failed", "failed", BAD),
        '    s_start -> provisioning [label="create request\\napplying"];',
        '    provisioning -> active [label="first apply ok"];',
        '    active -> updating [label="update / upgrade\\nrequest applying"];',
        '    updating -> active [label="apply ok"];',
        '    active -> drifted [label="drift.yml\\nexit code 2"];',
        '    drifted -> updating [label="reconcile PR"];',
        '    drifted -> active [label="adopt / clean\\nre-scan", style=dashed];',
        '    active -> destroying [label="destroy request\\n(manual or TTL)"];',
        '    destroying -> destroyed [label="destroy ok"];',
        '    provisioning -> s_failed [color="#B42318"];',
        '    updating -> s_failed [label="apply error", color="#B42318", fontcolor="#B42318"];',
        '    destroying -> s_failed [color="#B42318"];',
        '    s_failed -> updating [label="auto-retry / revert PR\\n(rollback)", style=dashed];',
        '    s_failed -> destroying [label="destroy", style=dashed];',
        "  }",
        # ---------------- request
        '  subgraph cluster_req { label="Change request (requests.status)"; fontsize=15; '
        'labeljust=l; style="rounded"; color="#9AA5B1"; bgcolor="#FBFCFE"; margin=18;',
        '    r_start [shape=circle, label="", width=0.22, style=filled, fillcolor="#1F2933", color="#1F2933"];',
        node("draft", "draft", NORMAL),
        node("pr_open", "pr_open", NORMAL),
        node("validating", "validating", NORMAL),
        node("plan_ready", "plan_ready", NORMAL),
        node("approved", "approved", NORMAL),
        node("applying", "applying", WARN),
        node("applied", "applied", GOOD, True),
        node("failed", "failed", BAD, True),
        node("rejected", "rejected", TERM, True),
        node("cancelled", "cancelled", TERM, True),
        '    r_start -> draft [label="portal submit"];',
        '    draft -> pr_open [label="generator + GitHub App\\nopen PR"];',
        '    pr_open -> validating [label="pr-validate.yml\\nstarted"];',
        '    validating -> plan_ready [label="all checks green;\\nplan + cost posted"];',
        '    validating -> failed [label="fmt / lint / policy /\\nbudget violation", color="#B42318", fontcolor="#B42318"];',
        '    plan_ready -> approved [label="CODEOWNERS (+ env\\napprover for prod)"];',
        '    approved -> applying [label="merged; apply.yml\\nlock acquired"];',
        '    applying -> applied [label="apply ok,\\noutputs reported"];',
        '    applying -> failed [label="apply error /\\nplan hash mismatch", color="#B42318", fontcolor="#B42318"];',
        '    plan_ready -> validating [label="new commit\\n(re-plan)", style=dashed];',
        '    applying -> applying [label="transient error\\nauto-retry (max 2)", style=dashed];',
        '    approved -> plan_ready [label="review\\ndismissed", style=dashed, constraint=false];',
        '    approved -> validating [label="new commit", style=dashed, constraint=false];',
        '    plan_ready -> rejected [label="changes\\nrequested / closed"];',
        '    pr_open -> cancelled [label="author\\ncancels"];',
        '    plan_ready -> cancelled [label="author\\ncancels", style=dashed];',
        '    { rank=same; applied; failed; }',
        "  }",
        '  { rank=same; r_start; s_start; }',
        '  s_start -> r_start [style=invis];',
        "}",
    ]
    return "\n".join(L)


if __name__ == "__main__":
    print("wrote", render_dot(build(), "state-machine-request"))
