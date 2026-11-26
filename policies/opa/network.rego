# METADATA
# title: No public data stores or tasks
# description: |
#   RDS instances must not be publicly accessible, and ECS services must not
#   give their tasks public IP addresses. Data stores and tasks live in
#   private subnets and are reached through security groups or a load
#   balancer.
package iacp.network

import data.lib.iacp as f

rds_instance_types := {"aws_db_instance", "aws_rds_cluster_instance"}

deny contains m if {
	some db in f.changed
	db.type in rds_instance_types
	f.flag(db, "publicly_accessible")
	not f.exempt("iacp.network.rds_public", db.address)
	m := f.msg(
		"iacp.network.rds_public", db.address,
		"database is publicly accessible",
		"set publicly_accessible = false and reach it from inside the VPC through its security group (the postgres module does this)",
	)
}

deny contains m if {
	some svc in f.changed_of_type("aws_ecs_service")
	some net in f.after(svc).network_configuration
	f.enabled(net.assign_public_ip)
	not f.exempt("iacp.network.ecs_public_ip", svc.address)
	m := f.msg(
		"iacp.network.ecs_public_ip", svc.address,
		"tasks get public IP addresses",
		"set assign_public_ip = false, run tasks in private subnets and expose them through a load balancer (the ecs-service module does this)",
	)
}
