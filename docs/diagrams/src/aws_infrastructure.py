"""AWS account and network topology for the platform and the workload accounts."""

from diagrams import Cluster, Diagram, Edge
from diagrams.aws.compute import ElasticContainerServiceService, Lambda
from diagrams.aws.database import RDSPostgresqlInstance
from diagrams.aws.integration import EventbridgeScheduler, SQS
from diagrams.aws.management import Cloudtrail, Cloudwatch
from diagrams.aws.network import ALB, NATGateway
from diagrams.aws.security import IAMRole, KMS, SecretsManager, WAF
from diagrams.aws.storage import S3
from diagrams.onprem.ci import GithubActions
from diagrams.onprem.client import Users

from common import BLUE, EDGE, GRAPH, GREEN, GREY, NODE, ORANGE, PURPLE, cluster, out

AMBER = "#B26B00"
PURPLE_E = "#6B46C1"


def workload(env: str, bg: str):
    with Cluster(f"{env} account", graph_attr=cluster(bg)):
        with Cluster("IAM roles (OIDC trust:\nrepo + branch / environment)",
                     graph_attr=cluster("white", style="dashed")):
            plan = IAMRole("iacp-plan\n(read-only)")
            apply = IAMRole("iacp-apply\n(boundary:\niacp-boundary)")
        with Cluster("Provisioned stacks (examples)", graph_attr=cluster("white", style="dashed")):
            s3 = S3("S3 + KMS")
            sqs = SQS("SQS + DLQ")
            fn = Lambda("Lambda")
        apply >> Edge(style="dotted") >> [s3, sqs, fn]
        trail = Cloudtrail("CloudTrail\n(org trail)")
        fn >> Edge(style="invis") >> trail
    return plan, apply, trail


def build():
    graph = dict(GRAPH, ranksep="0.9", nodesep="0.45", newrank="true")
    with Diagram("Self-Service IaC Platform: AWS infrastructure", filename=out("aws-infrastructure"),
                 show=False, direction="TB", graph_attr=graph, node_attr=NODE,
                 edge_attr=EDGE, outformat="png"):
        users = Users("Developers")
        gha = GithubActions("GitHub Actions\n(OIDC provider\ntoken.actions.\ngithubusercontent.com)")

        with Cluster("platform account (control plane)", graph_attr=cluster(BLUE)):
            with Cluster("VPC (3 AZs)", graph_attr=cluster("white")):
                with Cluster("Public subnets", graph_attr=cluster(GREEN)):
                    waf = WAF("AWS WAF")
                    alb = ALB("ALB (HTTPS)")
                    nat = NATGateway("NAT gateway")
                with Cluster("Private subnets: ECS Fargate", graph_attr=cluster(BLUE)):
                    portal = ElasticContainerServiceService("portal")
                    api = ElasticContainerServiceService("api")
                    worker = ElasticContainerServiceService("worker")
                with Cluster("Private data subnets", graph_attr=cluster(GREY)):
                    rds = RDSPostgresqlInstance("RDS PostgreSQL\nMulti-AZ")
            with Cluster("Regional services", graph_attr=cluster("white", style="dashed")):
                sqs = SQS("SQS events\n+ DLQ")
                sched = EventbridgeScheduler("EventBridge\nScheduler")
                secrets = SecretsManager("Secrets Manager\n(GitHub App key,\nDB creds)")
                cw = Cloudwatch("CloudWatch\nlogs / metrics / alarms")
            with Cluster("Terraform state", graph_attr=cluster("white", style="dashed")):
                state = S3("iacp-tfstate-<acct>\n(versioned, TLS-only,\nuse_lockfile)")
                kms = KMS("KMS CMK")

        with Cluster("Workload accounts", graph_attr=cluster("#FFFDF8")):
            dev_plan, dev_apply, t1 = workload("development", ORANGE)
            stg_plan, stg_apply, t2 = workload("staging", ORANGE)
            prd_plan, prd_apply, t3 = workload("production", "#FDEDEC")

        with Cluster("log-archive account", graph_attr=cluster(GREY)):
            archive = S3("CloudTrail archive\n(Object Lock)")

        users >> Edge(label="HTTPS") >> waf >> alb
        alb >> [portal, api]
        alb >> Edge(style="invis") >> worker
        waf >> Edge(style="invis") >> nat
        worker >> Edge(style="invis") >> sched
        api >> Edge(label="pgx (TLS)") >> rds
        worker >> rds
        api >> Edge(label="enqueue") >> sqs
        sqs >> Edge(label="consume", constraint="false") >> worker
        sched >> Edge(label="TTL / drift ticks", constraint="false") >> worker
        worker >> Edge(style="dotted") >> secrets
        [api, worker] >> Edge(style="dotted") >> cw
        worker >> Edge(label="GitHub API egress", style="dashed", constraint="false") >> nat
        state >> Edge(style="dotted") >> kms

        gha >> Edge(label="callbacks (OIDC JWT)", color=PURPLE_E) >> waf
        gha >> Edge(label="state r/w", color=AMBER) >> state
        for i, (p, a) in enumerate(((dev_plan, dev_apply), (stg_plan, stg_apply),
                                    (prd_plan, prd_apply))):
            gha >> Edge(label="AssumeRoleWithWebIdentity" if i == 1 else "",
                        color=AMBER) >> p
            gha >> Edge(color=AMBER) >> a
        for t in (t1, t2, t3):
            t >> Edge(color="#5B6675") >> archive

if __name__ == "__main__":
    build()
