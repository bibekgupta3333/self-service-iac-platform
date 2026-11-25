"""Internal components of the Go backend (API process + worker process)."""

from diagrams import Cluster, Diagram, Edge, Node
from diagrams.aws.integration import EventbridgeScheduler, SQS
from diagrams.aws.security import SecretsManager
from diagrams.onprem.ci import GithubActions
from diagrams.onprem.database import Postgresql
from diagrams.onprem.vcs import Github
from diagrams.programming.framework import Nextjs

from common import BLUE, EDGE, FONT, GRAPH, GREEN, GREY, NODE, ORANGE, PURPLE, cluster, out


def comp(label: str, fill: str = "#FFFFFF", border: str = "#1F3A5F") -> Node:
    return Node(label, shape="box", style="rounded,filled", fillcolor=fill, color=border,
                penwidth="1.4", fontname=FONT, fontsize="12", width="2.2", height="0.8",
                fixedsize="false", margin="0.16,0.08", labelloc="c", imagescale="false")


def build():
    graph = dict(GRAPH, ranksep="0.9", nodesep="0.35", compound="true")
    with Diagram("Foundry backend (Go): internal components",
                 filename=out("component-backend"), show=False, direction="LR",
                 graph_attr=graph, node_attr=NODE, edge_attr=EDGE, outformat="png"):
        portal = Nextjs("Portal")
        gh_hooks = Github("GitHub webhooks")
        actions = GithubActions("Actions callbacks\n(OIDC JWT)")

        with Cluster("cmd/api  (chi router)", graph_attr=cluster(BLUE)):
            with Cluster("HTTP edge", graph_attr=cluster("white", style="dashed")):
                mw = comp("Middleware\nrequest-id, JSON logs,\nrate limit, recover")
                auth = comp("Auth\nGitHub OAuth -> session/JWT\nRBAC from GitHub teams")
                oidc = comp("OIDC verifier\n(Actions JWT: repo,\nref, workflow claims)")
                hooks = comp("Webhook ingester\nHMAC verify -> SQS")
            with Cluster("Domain services", graph_attr=cluster("white", style="dashed")):
                catalog = comp("Catalog service\nmodules, versions,\nschemas, upgrades")
                reqs = comp("Request service\ncreate / update /\nupgrade / destroy")
                stacks = comp("Stack service\nstatus, outputs,\ndependency DAG")
                runs = comp("Run reporter\npipeline_runs, plan,\ncost, drift reports")
                policy = comp("Budget + RBAC\npolicy checks")
            with Cluster("Integration", graph_attr=cluster("white", style="dashed")):
                gen = comp("internal/generator\nHCL templates: main.tf,\nbackend.tf, versions.tf", GREEN, "#2E7D4F")
                ghc = comp("GitHub App client\nbranch, commit, PR,\nlabels, dispatch", PURPLE, "#4B3F8F")
                lock = comp("Lock manager\nper-stack lock rows\n+ queue")
                audit = comp("Audit writer\nappend-only,\nhash-chained", ORANGE, "#B26B00")
                repo = comp("Store (pgx + sqlc)\nrepositories, tx,\nmigrations", GREY, "#5B6675")

        with Cluster("cmd/worker", graph_attr=cluster(ORANGE)):
            consumer = comp("SQS consumer\n(idempotent handlers)")
            status = comp("Status updater\nPR / run -> request\nstate machine")
            ttl = comp("TTL reaper\nwarn 24h, open\ndestroy requests")
            drift = comp("Drift scheduler\ndispatch drift.yml\nmatrix")
            budget = comp("Budget checker\nmonthly spend vs\nteam budget")

        sqs = SQS("SQS")
        sched = EventbridgeScheduler("EventBridge\nScheduler")
        db = Postgresql("PostgreSQL")
        secrets = SecretsManager("Secrets Manager")
        github = Github("GitHub API")

        portal >> mw >> auth
        auth >> [catalog, reqs, stacks]
        gh_hooks >> hooks >> sqs
        actions >> oidc >> runs
        reqs >> policy
        reqs >> gen
        reqs >> lock
        gen >> ghc
        ghc >> Edge(color="#6B46C1") >> github
        stacks >> Edge(ltail="cluster_Domain services", label="all reads / writes") >> repo
        lock >> repo
        audit >> repo
        stacks >> Edge(ltail="cluster_Domain services", style="dotted") >> audit
        repo >> db
        ghc >> Edge(style="dotted") >> secrets

        sqs >> consumer >> status
        sched >> Edge(label="schedules") >> consumer
        consumer >> [ttl, drift, budget]
        budget >> Edge(ltail="cluster_cmd/worker", color="#5B6675") >> repo
        ttl >> Edge(label="destroy request", style="dashed") >> reqs
        drift >> Edge(label="dispatch", style="dashed") >> ghc


if __name__ == "__main__":
    build()
