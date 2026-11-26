package tests

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gruntwork-io/terratest/modules/terraform"
)

// TestS3Complete applies modules/s3/examples/complete to LocalStack and checks
// the secure defaults through the S3 API.
func TestS3Complete(t *testing.T) {
	t.Parallel()

	name := uniqueName("tt-s3")
	opts := exampleOptions(t, "s3", name)
	defer terraform.Destroy(t, opts)
	terraform.InitAndApply(t, opts)

	bucket := terraform.Output(t, opts, "bucket_id")
	logBucket := terraform.Output(t, opts, "access_log_bucket_id")
	kmsKeyArn := terraform.Output(t, opts, "kms_key_arn")
	if want := team + "-" + environment + "-" + name; bucket != want {
		t.Fatalf("bucket name = %q, want %q", bucket, want)
	}

	ctx := context.Background()
	client := s3.NewFromConfig(awsConfig(t), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint())
		o.UsePathStyle = true
	})

	t.Run("encryption", func(t *testing.T) {
		out, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: &bucket})
		if err != nil {
			t.Fatalf("GetBucketEncryption: %v", err)
		}
		rule := out.ServerSideEncryptionConfiguration.Rules[0]
		if rule.ApplyServerSideEncryptionByDefault.SSEAlgorithm != types.ServerSideEncryptionAwsKms {
			t.Errorf("SSE algorithm = %q, want aws:kms", rule.ApplyServerSideEncryptionByDefault.SSEAlgorithm)
		}
		if got := aws.ToString(rule.ApplyServerSideEncryptionByDefault.KMSMasterKeyID); got != kmsKeyArn {
			t.Errorf("KMS key = %q, want %q", got, kmsKeyArn)
		}
	})

	t.Run("public_access_block", func(t *testing.T) {
		out, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: &bucket})
		if err != nil {
			t.Fatalf("GetPublicAccessBlock: %v", err)
		}
		c := out.PublicAccessBlockConfiguration
		if !aws.ToBool(c.BlockPublicAcls) || !aws.ToBool(c.BlockPublicPolicy) || !aws.ToBool(c.IgnorePublicAcls) || !aws.ToBool(c.RestrictPublicBuckets) {
			t.Errorf("public access block not fully enabled: %+v", c)
		}
	})

	t.Run("versioning", func(t *testing.T) {
		out, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &bucket})
		if err != nil {
			t.Fatalf("GetBucketVersioning: %v", err)
		}
		if out.Status != types.BucketVersioningStatusEnabled {
			t.Errorf("versioning = %q, want Enabled", out.Status)
		}
	})

	t.Run("tls_only_policy", func(t *testing.T) {
		out, err := client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: &bucket})
		if err != nil {
			t.Fatalf("GetBucketPolicy: %v", err)
		}
		if !strings.Contains(aws.ToString(out.Policy), "aws:SecureTransport") {
			t.Errorf("bucket policy does not enforce TLS: %s", aws.ToString(out.Policy))
		}
	})

	t.Run("access_logging", func(t *testing.T) {
		out, err := client.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: &bucket})
		if err != nil {
			t.Fatalf("GetBucketLogging: %v", err)
		}
		if out.LoggingEnabled == nil || aws.ToString(out.LoggingEnabled.TargetBucket) != logBucket {
			t.Errorf("access logs not delivered to %q: %+v", logBucket, out.LoggingEnabled)
		}
	})

	t.Run("tags", func(t *testing.T) {
		out, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: &bucket})
		if err != nil {
			t.Fatalf("GetBucketTagging: %v", err)
		}
		tags := map[string]string{}
		for _, tag := range out.TagSet {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		want := map[string]string{"iacp:module": "s3", "cost-center": "cc-1234"}
		for k, v := range expectedTags {
			want[k] = v
		}
		if problems := tagProblems(tags, want); len(problems) > 0 {
			t.Errorf("bucket tags: %v (all tags: %v)", problems, tags)
		}
	})

	t.Run("object_round_trip", func(t *testing.T) {
		key := "terratest/hello.txt"
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: strings.NewReader("hello")}); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
		if err != nil {
			t.Fatalf("GetObject: %v", err)
		}
		defer got.Body.Close()
		body, _ := io.ReadAll(got.Body)
		if string(body) != "hello" {
			t.Errorf("object body = %q, want hello", body)
		}
		if got.ServerSideEncryption != types.ServerSideEncryptionAwsKms {
			t.Errorf("object SSE = %q, want aws:kms", got.ServerSideEncryption)
		}
	})
}
