package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/gruntwork-io/terratest/modules/terraform"
)

// TestSQSComplete applies modules/sqs/examples/complete to LocalStack and checks
// encryption, redrive and tags through the SQS API, then sends a message.
func TestSQSComplete(t *testing.T) {
	t.Parallel()

	name := uniqueName("tt-sqs")
	opts := exampleOptions(t, "sqs", name)
	defer terraform.Destroy(t, opts)
	terraform.InitAndApply(t, opts)

	queueURL := terraform.Output(t, opts, "queue_url")
	dlqURL := terraform.Output(t, opts, "dlq_url")
	dlqArn := terraform.Output(t, opts, "dlq_arn")
	queueArn := terraform.Output(t, opts, "queue_arn")
	kmsKeyArn := terraform.Output(t, opts, "kms_key_arn")

	ctx := context.Background()
	client := sqs.NewFromConfig(awsConfig(t), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(endpoint())
	})

	attrs := func(t *testing.T, url string) map[string]string {
		t.Helper()
		out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       &url,
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll},
		})
		if err != nil {
			t.Fatalf("GetQueueAttributes(%s): %v", url, err)
		}
		return out.Attributes
	}

	t.Run("main_queue", func(t *testing.T) {
		a := attrs(t, queueURL)
		if a["KmsMasterKeyId"] != kmsKeyArn {
			t.Errorf("KmsMasterKeyId = %q, want %q", a["KmsMasterKeyId"], kmsKeyArn)
		}
		if a["VisibilityTimeout"] != "60" || a["MessageRetentionPeriod"] != "86400" {
			t.Errorf("unexpected timings: visibility=%s retention=%s", a["VisibilityTimeout"], a["MessageRetentionPeriod"])
		}
		var redrive struct {
			DeadLetterTargetArn string `json:"deadLetterTargetArn"`
			MaxReceiveCount     any    `json:"maxReceiveCount"`
		}
		if err := json.Unmarshal([]byte(a["RedrivePolicy"]), &redrive); err != nil {
			t.Fatalf("parse RedrivePolicy %q: %v", a["RedrivePolicy"], err)
		}
		if redrive.DeadLetterTargetArn != dlqArn {
			t.Errorf("deadLetterTargetArn = %q, want %q", redrive.DeadLetterTargetArn, dlqArn)
		}
		if mrc, _ := json.Marshal(redrive.MaxReceiveCount); strings.Trim(string(mrc), `"`) != "3" {
			t.Errorf("maxReceiveCount = %s, want 3", mrc)
		}
		if !strings.Contains(a["Policy"], "aws:SecureTransport") {
			t.Errorf("queue policy does not enforce TLS: %s", a["Policy"])
		}
	})

	t.Run("dead_letter_queue", func(t *testing.T) {
		a := attrs(t, dlqURL)
		if a["KmsMasterKeyId"] != kmsKeyArn {
			t.Errorf("DLQ KmsMasterKeyId = %q, want %q", a["KmsMasterKeyId"], kmsKeyArn)
		}
		if a["MessageRetentionPeriod"] != "1209600" {
			t.Errorf("DLQ retention = %s, want 1209600", a["MessageRetentionPeriod"])
		}
		if !strings.Contains(a["RedriveAllowPolicy"], queueArn) {
			t.Errorf("DLQ redrive allow policy %q does not name %s", a["RedriveAllowPolicy"], queueArn)
		}
	})

	t.Run("tags", func(t *testing.T) {
		for url, role := range map[string]string{queueURL: "", dlqURL: "dlq"} {
			out, err := client.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(url)})
			if err != nil {
				t.Fatalf("ListQueueTags(%s): %v", url, err)
			}
			want := map[string]string{"iacp:module": "sqs", "cost-center": "cc-1234"}
			for k, v := range expectedTags {
				want[k] = v
			}
			if role != "" {
				want["iacp:role"] = role
			} else if _, ok := out.Tags["iacp:role"]; ok {
				t.Errorf("%s: main queue must not carry iacp:role", url)
			}
			if problems := tagProblems(out.Tags, want); len(problems) > 0 {
				t.Errorf("%s tags: %v (all tags: %v)", url, problems, out.Tags)
			}
		}
	})

	t.Run("send_receive", func(t *testing.T) {
		if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: &queueURL, MessageBody: aws.String("ping")}); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &queueURL, WaitTimeSeconds: 5, MaxNumberOfMessages: 1})
		if err != nil {
			t.Fatalf("ReceiveMessage: %v", err)
		}
		if len(out.Messages) != 1 || aws.ToString(out.Messages[0].Body) != "ping" {
			t.Fatalf("received %+v, want one message with body ping", out.Messages)
		}
		if _, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &queueURL, ReceiptHandle: out.Messages[0].ReceiptHandle}); err != nil {
			t.Errorf("DeleteMessage: %v", err)
		}
	})
}
