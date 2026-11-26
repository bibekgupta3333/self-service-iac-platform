package tests

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/gruntwork-io/terratest/modules/terraform"
)

// TestDynamoDBComplete applies modules/dynamodb/examples/complete to LocalStack
// and checks encryption, recovery, keys, indexes, TTL, streams and tags
// through the DynamoDB API, then writes and reads an item.
func TestDynamoDBComplete(t *testing.T) {
	t.Parallel()

	name := uniqueName("tt-ddb")
	opts := exampleOptions(t, "dynamodb", name)
	defer terraform.Destroy(t, opts)
	terraform.InitAndApply(t, opts)

	table := terraform.Output(t, opts, "table_name")
	tableArn := terraform.Output(t, opts, "table_arn")
	streamArn := terraform.Output(t, opts, "stream_arn")
	kmsKeyArn := terraform.Output(t, opts, "kms_key_arn")
	if want := team + "-" + environment + "-" + name; table != want {
		t.Fatalf("table name = %q, want %q", table, want)
	}

	ctx := context.Background()
	client := dynamodb.NewFromConfig(awsConfig(t), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint())
	})

	desc, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &table})
	if err != nil {
		t.Fatalf("DescribeTable: %v", err)
	}
	td := desc.Table

	t.Run("encryption", func(t *testing.T) {
		sse := td.SSEDescription
		if sse == nil || sse.Status != types.SSEStatusEnabled || sse.SSEType != types.SSETypeKms {
			t.Fatalf("SSEDescription = %+v, want ENABLED with KMS", sse)
		}
		if got := aws.ToString(sse.KMSMasterKeyArn); got != kmsKeyArn {
			t.Errorf("KMSMasterKeyArn = %q, want the module key %q", got, kmsKeyArn)
		}
	})

	t.Run("point_in_time_recovery", func(t *testing.T) {
		out, err := client.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{TableName: &table})
		if err != nil {
			t.Fatalf("DescribeContinuousBackups: %v", err)
		}
		pitr := out.ContinuousBackupsDescription.PointInTimeRecoveryDescription
		if pitr == nil || pitr.PointInTimeRecoveryStatus != types.PointInTimeRecoveryStatusEnabled {
			t.Errorf("point-in-time recovery = %+v, want ENABLED", pitr)
		}
	})

	t.Run("billing_and_deletion_protection", func(t *testing.T) {
		if td.BillingModeSummary == nil || td.BillingModeSummary.BillingMode != types.BillingModePayPerRequest {
			t.Errorf("BillingModeSummary = %+v, want PAY_PER_REQUEST", td.BillingModeSummary)
		}
		if aws.ToBool(td.DeletionProtectionEnabled) {
			t.Errorf("deletion protection is on, want off in %s", environment)
		}
	})

	t.Run("keys_and_indexes", func(t *testing.T) {
		keys := map[string]types.KeyType{}
		for _, k := range td.KeySchema {
			keys[aws.ToString(k.AttributeName)] = k.KeyType
		}
		if keys["pk"] != types.KeyTypeHash || keys["sk"] != types.KeyTypeRange || len(keys) != 2 {
			t.Errorf("KeySchema = %v, want pk HASH and sk RANGE", keys)
		}
		if len(td.GlobalSecondaryIndexes) != 1 {
			t.Fatalf("GlobalSecondaryIndexes = %d, want 1", len(td.GlobalSecondaryIndexes))
		}
		gsi := td.GlobalSecondaryIndexes[0]
		if aws.ToString(gsi.IndexName) != "by-status" || gsi.Projection == nil || gsi.Projection.ProjectionType != types.ProjectionTypeInclude {
			t.Errorf("index = %s %+v, want by-status with an INCLUDE projection", aws.ToString(gsi.IndexName), gsi.Projection)
		}
		attrs := map[string]types.ScalarAttributeType{}
		for _, a := range td.AttributeDefinitions {
			attrs[aws.ToString(a.AttributeName)] = a.AttributeType
		}
		want := map[string]types.ScalarAttributeType{"pk": "S", "sk": "S", "status": "S", "created_at": "N"}
		for k, v := range want {
			if attrs[k] != v {
				t.Errorf("attribute %s = %q, want %q (all: %v)", k, attrs[k], v, attrs)
			}
		}
	})

	t.Run("ttl_and_stream", func(t *testing.T) {
		ttl, err := client.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: &table})
		if err != nil {
			t.Fatalf("DescribeTimeToLive: %v", err)
		}
		d := ttl.TimeToLiveDescription
		if d == nil || d.TimeToLiveStatus != types.TimeToLiveStatusEnabled || aws.ToString(d.AttributeName) != "expires_at" {
			t.Errorf("TimeToLiveDescription = %+v, want ENABLED on expires_at", d)
		}
		spec := td.StreamSpecification
		if spec == nil || !aws.ToBool(spec.StreamEnabled) || spec.StreamViewType != types.StreamViewTypeNewAndOldImages {
			t.Errorf("StreamSpecification = %+v, want NEW_AND_OLD_IMAGES", spec)
		}
		if streamArn == "" || streamArn != aws.ToString(td.LatestStreamArn) {
			t.Errorf("stream_arn output = %q, want %q", streamArn, aws.ToString(td.LatestStreamArn))
		}
	})

	t.Run("tags", func(t *testing.T) {
		out, err := client.ListTagsOfResource(ctx, &dynamodb.ListTagsOfResourceInput{ResourceArn: &tableArn})
		if err != nil {
			t.Fatalf("ListTagsOfResource: %v", err)
		}
		tags := map[string]string{}
		for _, tag := range out.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		want := map[string]string{"iacp:module": "dynamodb", "cost-center": "cc-1234"}
		for k, v := range expectedTags {
			want[k] = v
		}
		if problems := tagProblems(tags, want); len(problems) > 0 {
			t.Errorf("table tags: %v (all tags: %v)", problems, tags)
		}
	})

	t.Run("put_get_item", func(t *testing.T) {
		item := map[string]types.AttributeValue{
			"pk":     &types.AttributeValueMemberS{Value: "order#1"},
			"sk":     &types.AttributeValueMemberS{Value: "v1"},
			"status": &types.AttributeValueMemberS{Value: "open"},
		}
		if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: item}); err != nil {
			t.Fatalf("PutItem: %v", err)
		}
		out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName:      &table,
			Key:            map[string]types.AttributeValue{"pk": item["pk"], "sk": item["sk"]},
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		status, ok := out.Item["status"].(*types.AttributeValueMemberS)
		if !ok || status.Value != "open" {
			t.Errorf("GetItem returned %v, want status=open", out.Item)
		}
	})
}
