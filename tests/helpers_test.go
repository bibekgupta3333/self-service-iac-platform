// Package tests holds Terratest integration tests that apply module examples
// to LocalStack (ADR-0009) and assert on the resulting AWS resources.
package tests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/gruntwork-io/terratest/modules/terraform"
)

const (
	defaultEndpoint = "http://localhost:4566"
	region          = "us-east-1"
	team            = "platform"
	environment     = "development"
)

// requiredTags must be present on every taggable resource (docs/07 section 6.3).
var requiredTags = []string{"iacp:team", "iacp:environment", "iacp:stack", "iacp:managed-by"}

// endpoint returns the LocalStack edge URL, overridable with LOCALSTACK_ENDPOINT.
func endpoint() string {
	if v := os.Getenv("LOCALSTACK_ENDPOINT"); v != "" {
		return v
	}
	return defaultEndpoint
}

// awsConfig builds an SDK config with LocalStack's dummy credentials. Clients
// set BaseEndpoint themselves so nothing can reach real AWS.
func awsConfig(t *testing.T) aws.Config {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	return cfg
}

// uniqueName returns a lowercase name that will not collide between runs.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, strings.ToLower(random.UniqueId()))
}

// exampleOptions returns Terraform options for modules/<module>/examples/complete.
func exampleOptions(t *testing.T, module, name string) *terraform.Options {
	t.Helper()
	return terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: fmt.Sprintf("../modules/%s/examples/complete", module),
		Vars: map[string]interface{}{
			"name":                name,
			"team":                team,
			"environment":         environment,
			"stack":               "terratest",
			"localstack_endpoint": endpoint(),
		},
		EnvVars: map[string]string{
			"AWS_ACCESS_KEY_ID":     "test",
			"AWS_SECRET_ACCESS_KEY": "test",
			"AWS_DEFAULT_REGION":    region,
		},
		NoColor: true,
	})
}

// missingTags returns the required tags absent from tags.
func missingTags(tags map[string]string) []string {
	var missing []string
	for _, k := range requiredTags {
		if _, ok := tags[k]; !ok {
			missing = append(missing, k)
		}
	}
	return missing
}

func TestMissingTags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tags map[string]string
		want int
	}{
		{"all present", map[string]string{"iacp:team": "a", "iacp:environment": "b", "iacp:stack": "c", "iacp:managed-by": "iacp"}, 0},
		{"none", map[string]string{}, 4},
		{"one missing", map[string]string{"iacp:team": "a", "iacp:environment": "b", "iacp:stack": "c"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(missingTags(tc.tags)); got != tc.want {
				t.Fatalf("missingTags() returned %d, want %d", got, tc.want)
			}
		})
	}
}
