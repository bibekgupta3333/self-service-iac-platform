package iacp.network_test

import data.iacp.network as net
import data.lib.testing as t

# --- RDS -----------------------------------------------------------------------

test_private_rds_instance_allowed if {
	count(net.deny) == 0 with input as t.plan([t.rc("aws_db_instance", "db", {"publicly_accessible": false})])
}

test_public_rds_instance_denied if {
	some m in net.deny with input as t.plan([t.rc_in("orders", "aws_db_instance", "this", {"publicly_accessible": true})])
	m == concat("", [
		"[iacp.network.rds_public] module.orders.aws_db_instance.this: database is publicly accessible. ",
		"Fix: set publicly_accessible = false and reach it from inside the VPC through its security group (the postgres module does this)",
	])
}

test_public_aurora_instance_denied if {
	r := t.rc("aws_rds_cluster_instance", "writer", {"publicly_accessible": true})
	t.rule_ids(net.deny) == {"[iacp.network.rds_public]"} with input as t.plan([r])
}

test_public_rds_instance_being_deleted_ignored if {
	r := t.with_actions(t.rc("aws_db_instance", "db", {"publicly_accessible": true}), ["delete"])
	count(net.deny) == 0 with input as t.plan([r])
}

test_public_rds_instance_exception_honoured if {
	r := t.rc("aws_db_instance", "db", {"publicly_accessible": true})
	ctx := object.union(t.dev_ctx, {
		"now": "2026-11-26T00:00:00Z",
		"exceptions": [{"rule_id": "iacp.network.rds_public", "resource_address": "aws_db_instance.db", "expires_at": "2026-12-31T00:00:00Z"}],
	})
	count(net.deny) == 0 with input as t.plan([r]) with data.iacp.context as ctx
}

# --- ECS -----------------------------------------------------------------------

service(public_ip) := t.rc("aws_ecs_service", "svc", {"network_configuration": [{"assign_public_ip": public_ip, "subnets": ["subnet-1"]}]})

test_ecs_service_without_public_ip_allowed if {
	count(net.deny) == 0 with input as t.plan([service(false)])
}

test_ecs_service_without_network_configuration_allowed if {
	count(net.deny) == 0 with input as t.plan([t.rc("aws_ecs_service", "svc", {"network_configuration": []})])
}

test_ecs_service_with_public_ip_denied if {
	some m in net.deny with input as t.plan([service(true)])
	startswith(m, "[iacp.network.ecs_public_ip] aws_ecs_service.svc: tasks get public IP addresses. Fix:")
}
