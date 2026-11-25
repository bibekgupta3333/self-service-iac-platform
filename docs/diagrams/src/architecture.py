"""Logical / system architecture of the Foundry platform."""

from diagrams import Cluster, Diagram, Edge
from diagrams.aws.integration import EventbridgeScheduler, SQS
from diagrams.aws.security import IAMRole
from diagrams.aws.storage import S3
from diagrams.aws.general import GenericOfficeBuilding
from diagrams.onprem.ci import GithubActions
from diagrams.onprem.client import Users
from diagrams.onprem.database import Postgresql
from diagrams.onprem.vcs import Github
from diagrams.programming.framework import Nextjs
from diagrams.programming.language import Go

from common import BLUE, EDGE, GRAPH, GREEN, NODE, ORANGE, PURPLE, cluster, out


def build():
    graph = dict(GRAPH, ranksep="1.1", nodesep="0.8")
    with Diagram("Foundry: system architecture", filename=out("architecture"),
                 show=False, direction="LR", graph_attr=graph, node_attr=NODE,
                 edge_attr=EDGE, outformat="png"):
        dev = Users("Developers /\nmaintainers")

        with Cluster("Platform AWS account: Foundry control plane", graph_attr=cluster(BLUE)):
            portal = Nextjs("Developer portal\n(Next.js)")
            with Cluster("Go backend", graph_attr=cluster("#E6EEF9")):
                api = Go("Foundry API\n(REST, auth, catalog,\nrequests, generator)")
                worker = Go("Worker\n(status, TTL reaper,\ndrift, budgets)")
            queue = SQS("SQS\nevents queue")
            db = Postgresql("PostgreSQL\n(catalog, stacks,\nrequests, audit)")
            sched = EventbridgeScheduler("EventBridge\nScheduler")

        with Cluster("GitHub", graph_attr=cluster(PURPLE)):
            app = Github("GitHub App + foundry monorepo\n(PR review + merge by humans)")
            actions = GithubActions("GitHub Actions\npr-validate / apply /\ndrift / ttl / release")

        with Cluster("Platform AWS account: Terraform state", graph_attr=cluster(BLUE)):
            state = S3("S3 foundry-tfstate-<acct>\n<env>/<team>/<stack>/\nterraform.tfstate")

        with Cluster("Workload AWS accounts", graph_attr=cluster(ORANGE)):
            roles = IAMRole("foundry-plan /\nfoundry-apply roles")
            envs = GenericOfficeBuilding("development / staging /\nproduction resources")

        purple, amber = "#6B46C1", "#B26B00"
        # main left-to-right spine
        dev >> Edge(label="HTTPS\n(GitHub OAuth)") >> portal
        portal >> Edge(label="REST/JSON") >> api
        api >> Edge(label="branch, commit, PR,\nworkflow_dispatch") >> app
        app >> Edge(label="runs workflows") >> actions
        actions >> Edge(label="OIDC\nAssumeRoleWithWebIdentity", color=amber) >> roles
        roles >> Edge(label="plan / apply") >> envs
        # data + async paths inside the control plane
        api >> Edge(label="pgx / sqlc") >> db
        api >> Edge(label="webhook events") >> queue
        queue >> Edge(label="consume") >> worker
        sched >> Edge(label="TTL / drift\nschedules") >> worker
        worker >> Edge(constraint="false") >> db
        actions >> Edge(label="state + lockfile\n(SSE-KMS)", color=amber) >> state
        # feedback paths (do not affect ranking)
        app >> Edge(label="webhooks (HMAC)", color=purple, constraint="false") >> api
        actions >> Edge(label="status, plan, outputs\n(GitHub OIDC JWT)", color=purple,
                        constraint="false") >> api
        worker >> Edge(label="destroy PR /\ndrift dispatch", style="dashed") >> app
        db >> Edge(style="invis") >> app

if __name__ == "__main__":
    build()
