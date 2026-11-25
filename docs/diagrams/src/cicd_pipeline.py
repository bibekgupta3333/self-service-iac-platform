"""CI/CD pipeline: PR validation stages and apply-on-merge stages."""

from diagrams import Cluster, Diagram, Edge
from diagrams.onprem.client import User
from diagrams.onprem.vcs import Github
from diagrams.programming.language import Go

from common import EDGE, FONT, GRAPH, GREEN, NODE, ORANGE, PURPLE, cluster, out
from diagrams import Node


def step(label: str, fill: str = "#FFFFFF", border: str = "#1F3A5F") -> Node:
    """A plain rounded box used for pipeline stages."""
    return Node(label, shape="box", style="rounded,filled", fillcolor=fill,
                color=border, penwidth="1.5", fontname=FONT, fontsize="13",
                width="1.9", height="0.85", fixedsize="false", margin="0.18,0.1",
                labelloc="c", imagescale="false")


GATE = "#FFF4E5"
OK = "#E7F4EC"
TOOL = "#FFFFFF"


def build():
    graph = dict(GRAPH, ranksep="0.6", nodesep="0.35", splines="ortho", newrank="true", forcelabels="true")
    with Diagram("Foundry: CI/CD pipeline (pr-validate.yml and apply.yml)",
                 filename=out("cicd-pipeline"), show=False, direction="LR",
                 graph_attr=graph, node_attr=NODE, edge_attr=dict(EDGE, penwidth="1.4"),
                 outformat="png"):
        with Cluster("pr-validate.yml  (on pull_request; foundry-plan role, read-only)",
                     graph_attr=cluster(PURPLE)):
            fmt = step("1. terraform fmt\n-check")
            val = step("2. terraform\nvalidate")
            lint = step("3. TFLint\n(aws ruleset)")
            ckv = step("4. Checkov\n(+ custom checks)")
            plan = step("5. terraform plan\n-out + show -json")
            opa = step("6. OPA / conftest\non plan JSON")
            cost = step("7. Infracost\n+ budget policy")
            report = step("8. PR comment +\nreport to API")
            fmt >> val >> lint >> ckv >> plan >> opa >> cost >> report

        with Cluster("Merge gates", graph_attr=cluster(ORANGE)):
            gate = step("Required checks green\n+ CODEOWNERS approval\n"
                        "(production: GitHub\nEnvironment approver)\n-> merge to main", GATE, "#B26B00")

        with Cluster("apply.yml  (on push to main; changed stacks only; foundry-apply role)",
                     graph_attr=cluster(GREEN)):
            lock = step("a. acquire stack lock\n(API, Postgres row)")
            replan = step("b. re-plan +\nverify plan hash")
            apply = step("c. terraform apply\n(S3 lockfile)")
            smoke = step("d. smoke tests /\nTerratest (ephemeral)")
            outputs = step("e. outputs + status\nto API; release lock", OK, "#2E7D4F")
            lock >> replan >> apply >> smoke >> outputs

        dev = User("Developer\n(portal request)")
        pr = Github("Pull request\nopened / updated")
        api = Go("Foundry API\n(callbacks with GitHub\nOIDC JWT: status, plan,\ncost, outputs, audit)")
        dev >> pr >> fmt
        # second row: merge gate + apply, laid out under the PR row
        pr >> Edge(style="invis") >> gate
        report >> Edge(xlabel="all checks pass", constraint="false") >> gate
        gate >> lock
        outputs >> Edge(style="dashed", color="#6B46C1") >> api
        report >> Edge(style="dashed", color="#6B46C1",
                       constraint="false") >> api

if __name__ == "__main__":
    build()
