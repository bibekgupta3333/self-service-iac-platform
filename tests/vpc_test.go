package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/gruntwork-io/terratest/modules/terraform"
)

// ec2Tags flattens EC2 tags into a map.
func ec2Tags(tags []types.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, tag := range tags {
		out[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return out
}

// TestVPCComplete applies modules/vpc/examples/complete to LocalStack and checks
// DNS, subnets, the default security group, NAT routing, flow logs and tags
// through the EC2 API.
func TestVPCComplete(t *testing.T) {
	t.Parallel()

	name := uniqueName("tt-vpc")
	opts := exampleOptions(t, "vpc", name)
	defer terraform.Destroy(t, opts)
	terraform.InitAndApply(t, opts)

	vpcID := terraform.Output(t, opts, "vpc_id")
	publicSubnets := terraform.OutputList(t, opts, "public_subnet_ids")
	privateSubnets := terraform.OutputList(t, opts, "private_subnet_ids")
	privateRouteTables := terraform.OutputList(t, opts, "private_route_table_ids")
	natGateways := terraform.OutputList(t, opts, "nat_gateway_ids")
	defaultSG := terraform.Output(t, opts, "default_security_group_id")
	flowLogGroup := terraform.Output(t, opts, "flow_log_group_name")

	ctx := context.Background()
	client := ec2.NewFromConfig(awsConfig(t), func(o *ec2.Options) {
		o.BaseEndpoint = aws.String(endpoint())
	})

	baseTags := map[string]string{"iacp:module": "vpc", "cost-center": "cc-1234"}
	for k, v := range expectedTags {
		baseTags[k] = v
	}
	withTier := func(tier string) map[string]string {
		want := map[string]string{"iacp:tier": tier}
		for k, v := range baseTags {
			want[k] = v
		}
		return want
	}

	t.Run("vpc", func(t *testing.T) {
		out, err := client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
		if err != nil || len(out.Vpcs) != 1 {
			t.Fatalf("DescribeVpcs(%s): %v (%d found)", vpcID, err, len(out.Vpcs))
		}
		if got := aws.ToString(out.Vpcs[0].CidrBlock); got != "10.60.0.0/16" {
			t.Errorf("CidrBlock = %q, want 10.60.0.0/16", got)
		}
		tags := ec2Tags(out.Vpcs[0].Tags)
		if problems := tagProblems(tags, baseTags); len(problems) > 0 {
			t.Errorf("VPC tags: %v (all tags: %v)", problems, tags)
		}
		if want := team + "-" + environment + "-" + name; tags["Name"] != want {
			t.Errorf("Name tag = %q, want %q", tags["Name"], want)
		}
		for _, attr := range []types.VpcAttributeName{types.VpcAttributeNameEnableDnsSupport, types.VpcAttributeNameEnableDnsHostnames} {
			a, err := client.DescribeVpcAttribute(ctx, &ec2.DescribeVpcAttributeInput{VpcId: &vpcID, Attribute: attr})
			if err != nil {
				t.Fatalf("DescribeVpcAttribute(%s): %v", attr, err)
			}
			enabled := (attr == types.VpcAttributeNameEnableDnsSupport && a.EnableDnsSupport != nil && aws.ToBool(a.EnableDnsSupport.Value)) ||
				(attr == types.VpcAttributeNameEnableDnsHostnames && a.EnableDnsHostnames != nil && aws.ToBool(a.EnableDnsHostnames.Value))
			if !enabled {
				t.Errorf("%s is off, want on", attr)
			}
		}
	})

	t.Run("subnets", func(t *testing.T) {
		if len(publicSubnets) != 3 || len(privateSubnets) != 3 {
			t.Fatalf("got %d public and %d private subnets, want 3 each", len(publicSubnets), len(privateSubnets))
		}
		for tier, ids := range map[string][]string{"public": publicSubnets, "private": privateSubnets} {
			out, err := client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: ids})
			if err != nil {
				t.Fatalf("DescribeSubnets(%s): %v", tier, err)
			}
			zones := map[string]bool{}
			for _, s := range out.Subnets {
				if aws.ToBool(s.MapPublicIpOnLaunch) {
					t.Errorf("%s subnet %s assigns public IPs on launch", tier, aws.ToString(s.SubnetId))
				}
				if aws.ToString(s.VpcId) != vpcID {
					t.Errorf("%s subnet %s is in VPC %s, want %s", tier, aws.ToString(s.SubnetId), aws.ToString(s.VpcId), vpcID)
				}
				zones[aws.ToString(s.AvailabilityZone)] = true
				tags := ec2Tags(s.Tags)
				if problems := tagProblems(tags, withTier(tier)); len(problems) > 0 {
					t.Errorf("%s subnet %s tags: %v (all tags: %v)", tier, aws.ToString(s.SubnetId), problems, tags)
				}
			}
			if len(zones) != 3 {
				t.Errorf("%s subnets span %d zones, want 3", tier, len(zones))
			}
		}
	})

	t.Run("default_security_group", func(t *testing.T) {
		out, err := client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{defaultSG}})
		if err != nil || len(out.SecurityGroups) != 1 {
			t.Fatalf("DescribeSecurityGroups(%s): %v", defaultSG, err)
		}
		sg := out.SecurityGroups[0]
		if aws.ToString(sg.GroupName) != "default" || aws.ToString(sg.VpcId) != vpcID {
			t.Errorf("security group %s is %q in %s, want the default group of %s", defaultSG, aws.ToString(sg.GroupName), aws.ToString(sg.VpcId), vpcID)
		}
		if len(sg.IpPermissions) != 0 || len(sg.IpPermissionsEgress) != 0 {
			t.Errorf("default security group has %d ingress and %d egress rules, want none", len(sg.IpPermissions), len(sg.IpPermissionsEgress))
		}
	})

	t.Run("nat_routing", func(t *testing.T) {
		if len(natGateways) != 3 {
			t.Fatalf("got %d NAT gateways, want one per zone", len(natGateways))
		}
		nats, err := client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: natGateways})
		if err != nil {
			t.Fatalf("DescribeNatGateways: %v", err)
		}
		natSubnet := map[string]string{}
		for _, n := range nats.NatGateways {
			natSubnet[aws.ToString(n.NatGatewayId)] = aws.ToString(n.SubnetId)
			if tags := ec2Tags(n.Tags); len(tagProblems(tags, baseTags)) > 0 {
				t.Errorf("NAT gateway %s tags: %v", aws.ToString(n.NatGatewayId), tagProblems(tags, baseTags))
			}
		}
		public := map[string]bool{}
		for _, id := range publicSubnets {
			public[id] = true
		}
		out, err := client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: privateRouteTables})
		if err != nil {
			t.Fatalf("DescribeRouteTables: %v", err)
		}
		used := map[string]bool{}
		for _, rt := range out.RouteTables {
			var via string
			for _, r := range rt.Routes {
				if aws.ToString(r.DestinationCidrBlock) == "0.0.0.0/0" {
					via = aws.ToString(r.NatGatewayId)
					if r.GatewayId != nil && aws.ToString(r.GatewayId) != "local" {
						t.Errorf("private route table %s routes to gateway %s", aws.ToString(rt.RouteTableId), aws.ToString(r.GatewayId))
					}
				}
			}
			if !public[natSubnet[via]] {
				t.Errorf("private route table %s default route uses %q, want a NAT gateway in a public subnet", aws.ToString(rt.RouteTableId), via)
			}
			used[via] = true
		}
		if len(used) != 3 {
			t.Errorf("private route tables use %d NAT gateways, want one each", len(used))
		}
	})

	t.Run("flow_logs", func(t *testing.T) {
		out, err := client.DescribeFlowLogs(ctx, &ec2.DescribeFlowLogsInput{
			Filter: []types.Filter{{Name: aws.String("resource-id"), Values: []string{vpcID}}},
		})
		if err != nil {
			t.Fatalf("DescribeFlowLogs: %v", err)
		}
		if len(out.FlowLogs) != 1 {
			t.Fatalf("got %d flow logs for %s, want 1", len(out.FlowLogs), vpcID)
		}
		fl := out.FlowLogs[0]
		// LocalStack returns the destination ARN but not LogGroupName for
		// log_destination-based flow logs, so match the ARN's log group.
		dest := aws.ToString(fl.LogDestination)
		if !strings.HasSuffix(strings.TrimSuffix(dest, ":*"), ":log-group:"+flowLogGroup) || fl.TrafficType != types.TrafficTypeReject {
			t.Errorf("flow log = destination %q traffic %s, want log group %q and REJECT", dest, fl.TrafficType, flowLogGroup)
		}
		if fl.LogDestinationType != "" && fl.LogDestinationType != types.LogDestinationTypeCloudWatchLogs {
			t.Errorf("flow log destination type = %s, want cloud-watch-logs", fl.LogDestinationType)
		}
		if tags := ec2Tags(fl.Tags); len(tagProblems(tags, baseTags)) > 0 {
			t.Errorf("flow log tags: %v (all tags: %v)", tagProblems(tags, baseTags), tags)
		}
	})
}
