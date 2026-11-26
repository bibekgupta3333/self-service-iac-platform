// Package tests holds Terratest integration tests that apply module examples
// to LocalStack (ADR-0009) and assert on the resulting AWS resources.
package tests

import (
	"context"
	"fmt"
	"os"
	"sort"
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

// stack is the iacp:stack value the examples are applied with.
const stack = "terratest"

// expectedTags are the platform tags (and values) every taggable resource
// must carry, mirroring policies/opa/tags.rego with a matching stack context.
var expectedTags = map[string]string{
	"iacp:team":        team,
	"iacp:environment": environment,
	"iacp:stack":       stack,
	"iacp:managed-by":  "iacp",
}

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
			"stack":               stack,
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

// tagProblems describes every expected platform tag that is missing or has
// the wrong value in tags, sorted for stable output.
func tagProblems(tags, want map[string]string) []string {
	var problems []string
	for k, v := range want {
		got, ok := tags[k]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s missing", k))
		case got != v:
			problems = append(problems, fmt.Sprintf("%s=%q, want %q", k, got, v))
		}
	}
	sort.Strings(problems)
	return problems
}

func TestTagProblems(t *testing.T) {
	t.Parallel()
	want := map[string]string{"iacp:team": "platform", "iacp:stack": "terratest"}
	cases := []struct {
		name string
		tags map[string]string
		want []string
	}{
		{"all match", map[string]string{"iacp:team": "platform", "iacp:stack": "terratest", "extra": "x"}, nil},
		{"missing", map[string]string{"iacp:team": "platform"}, []string{"iacp:stack missing"}},
		{"wrong value", map[string]string{"iacp:team": "payments", "iacp:stack": "terratest"}, []string{`iacp:team="payments", want "platform"`}},
		{"empty value", map[string]string{"iacp:team": "", "iacp:stack": "terratest"}, []string{`iacp:team="", want "platform"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tagProblems(tc.tags, want)
			if strings.Join(got, ";") != strings.Join(tc.want, ";") {
				t.Fatalf("tagProblems() = %v, want %v", got, tc.want)
			}
		})
	}
}
