package tests

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/gruntwork-io/terratest/modules/terraform"
)

// TestMonitoringComplete applies modules/monitoring/examples/complete to
// LocalStack and checks the alarm topic, its subscription, the alarms and the
// dashboard through the SNS and CloudWatch APIs.
func TestMonitoringComplete(t *testing.T) {
	t.Parallel()

	name := uniqueName("tt-mon")
	opts := exampleOptions(t, "monitoring", name)
	defer terraform.Destroy(t, opts)
	terraform.InitAndApply(t, opts)

	topicArn := terraform.Output(t, opts, "sns_topic_arn")
	kmsKeyArn := terraform.Output(t, opts, "kms_key_arn")
	dashboard := terraform.Output(t, opts, "dashboard_name")
	alarmArns := terraform.OutputMap(t, opts, "alarm_arns")
	prefix := team + "-" + environment + "-" + name

	ctx := context.Background()
	cfg := awsConfig(t)
	snsClient := sns.NewFromConfig(cfg, func(o *sns.Options) { o.BaseEndpoint = aws.String(endpoint()) })
	cw := cloudwatch.NewFromConfig(cfg, func(o *cloudwatch.Options) { o.BaseEndpoint = aws.String(endpoint()) })

	t.Run("topic", func(t *testing.T) {
		out, err := snsClient.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: &topicArn})
		if err != nil {
			t.Fatalf("GetTopicAttributes: %v", err)
		}
		if got := out.Attributes["KmsMasterKeyId"]; got != kmsKeyArn {
			t.Errorf("KmsMasterKeyId = %q, want the module key %q", got, kmsKeyArn)
		}
		policy := out.Attributes["Policy"]
		if !strings.Contains(policy, "aws:SecureTransport") || !strings.Contains(policy, "cloudwatch.amazonaws.com") {
			t.Errorf("topic policy must allow CloudWatch and deny non-TLS publishing: %s", policy)
		}
	})

	t.Run("tags", func(t *testing.T) {
		out, err := snsClient.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{ResourceArn: &topicArn})
		if err != nil {
			t.Fatalf("ListTagsForResource: %v", err)
		}
		tags := map[string]string{}
		for _, tag := range out.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		want := map[string]string{"iacp:module": "monitoring", "cost-center": "cc-1234"}
		for k, v := range expectedTags {
			want[k] = v
		}
		if problems := tagProblems(tags, want); len(problems) > 0 {
			t.Errorf("topic tags: %v (all tags: %v)", problems, tags)
		}
	})

	t.Run("subscription", func(t *testing.T) {
		out, err := snsClient.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{TopicArn: &topicArn})
		if err != nil {
			t.Fatalf("ListSubscriptionsByTopic: %v", err)
		}
		if len(out.Subscriptions) != 1 || aws.ToString(out.Subscriptions[0].Protocol) != "email" || aws.ToString(out.Subscriptions[0].Endpoint) != "on-call@example.com" {
			t.Errorf("subscriptions = %+v, want one email subscription to on-call@example.com", out.Subscriptions)
		}
	})

	t.Run("alarms", func(t *testing.T) {
		wantKeys := []string{"lambda-worker-errors", "lambda-worker-throttles", "metric-jobs-failed", "sqs-jobs-oldest-message-age", "sqs-jobs-visible-messages"}
		var keys []string
		for k := range alarmArns {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
			t.Fatalf("alarm_arns keys = %v, want %v", keys, wantKeys)
		}
		names := make([]string, 0, len(wantKeys))
		for _, k := range wantKeys {
			names = append(names, prefix+"-"+k)
		}
		out, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: names})
		if err != nil {
			t.Fatalf("DescribeAlarms: %v", err)
		}
		if len(out.MetricAlarms) != len(names) {
			t.Fatalf("found %d of the alarms %v", len(out.MetricAlarms), names)
		}
		for _, a := range out.MetricAlarms {
			if len(a.AlarmActions) != 1 || a.AlarmActions[0] != topicArn || len(a.OKActions) != 1 || a.OKActions[0] != topicArn {
				t.Errorf("alarm %s actions = %v / %v, want the topic on alarm and OK", aws.ToString(a.AlarmName), a.AlarmActions, a.OKActions)
			}
			if aws.ToString(a.AlarmName) == prefix+"-metric-jobs-failed" {
				if aws.ToString(a.Namespace) != "Example/Jobs" || aws.ToString(a.MetricName) != "JobsFailed" || string(a.Statistic) != "Sum" || aws.ToInt32(a.Period) != 600 || aws.ToFloat64(a.Threshold) != 5 {
					t.Errorf("metric alarm = %s %s %s period %d threshold %v, want Example/Jobs JobsFailed Sum period 600 threshold 5",
						aws.ToString(a.Namespace), aws.ToString(a.MetricName), a.Statistic, aws.ToInt32(a.Period), aws.ToFloat64(a.Threshold))
				}
			}
		}
	})

	t.Run("dashboard", func(t *testing.T) {
		if dashboard != prefix {
			t.Fatalf("dashboard_name = %q, want %q", dashboard, prefix)
		}
		out, err := cw.GetDashboard(ctx, &cloudwatch.GetDashboardInput{DashboardName: &dashboard})
		if err != nil {
			t.Fatalf("GetDashboard: %v", err)
		}
		var body struct {
			Widgets []struct {
				Type       string         `json:"type"`
				Properties map[string]any `json:"properties"`
			} `json:"widgets"`
		}
		if err := json.Unmarshal([]byte(aws.ToString(out.DashboardBody)), &body); err != nil {
			t.Fatalf("parse dashboard body: %v", err)
		}
		var types []string
		for _, w := range body.Widgets {
			types = append(types, w.Type)
		}
		if strings.Join(types, ",") != "text,metric,alarm" {
			t.Fatalf("widget types = %v, want text, metric, alarm", types)
		}
		if alarms, _ := body.Widgets[2].Properties["alarms"].([]any); len(alarms) != len(alarmArns) {
			t.Errorf("alarm widget lists %d alarms, want %d", len(alarms), len(alarmArns))
		}
	})
}
