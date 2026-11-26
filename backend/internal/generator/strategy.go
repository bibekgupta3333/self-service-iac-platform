package generator

// BackendStrategy decides where Terraform state lives (ADR-0009: local now, S3 later).
type BackendStrategy interface {
	// Name is recorded in iacp.stack.json.
	Name() string
	// Backend returns the backend block type and attributes for a stack. Pure.
	Backend(id StackIdentity) (blockType string, attrs []Attr)
}

// LocalBackend keeps state next to the stack root. The root lives at
// environments/<env>/<team>/<stack>/, so the state file follows the production key
// layout. State files are git-ignored.
type LocalBackend struct{}

// Name implements BackendStrategy.
func (LocalBackend) Name() string { return "local" }

// Backend implements BackendStrategy.
func (LocalBackend) Backend(StackIdentity) (string, []Attr) {
	return "local", []Attr{{Name: "path", Value: "terraform.tfstate"}}
}

// S3Backend stores state in the platform state bucket with S3-native locking
// (ADR-0004). Implemented now so the switch is configuration; unused while local-first.
type S3Backend struct {
	Bucket   string
	Region   string
	KMSKeyID string
}

// Name implements BackendStrategy.
func (S3Backend) Name() string { return "s3" }

// Backend implements BackendStrategy.
func (b S3Backend) Backend(id StackIdentity) (string, []Attr) {
	return "s3", []Attr{
		{Name: "bucket", Value: b.Bucket},
		{Name: "key", Value: id.StateKey()},
		{Name: "region", Value: b.Region},
		{Name: "encrypt", Value: true},
		{Name: "kms_key_id", Value: b.KMSKeyID},
		{Name: "use_lockfile", Value: true},
	}
}

// ProviderConfig is the rendered shape of the aws provider block.
type ProviderConfig struct {
	Attrs     []Attr            // top-level attributes after region
	Endpoints map[string]string // endpoints {} block; empty = omitted
}

// ProviderStrategy decides how the aws provider reaches AWS (or LocalStack).
type ProviderStrategy interface {
	Provider(id StackIdentity) ProviderConfig
}

// LocalStackServices are the AWS services routed to LocalStack (matches docker compose).
var LocalStackServices = []string{
	"cloudwatch", "cloudwatchlogs", "eventbridge", "iam", "kms", "lambda", "s3", "secretsmanager", "sns", "sqs", "sts",
}

// LocalStackProvider points every service at a LocalStack endpoint. Credentials are
// not rendered: the caller's environment supplies LocalStack's dummy test/test pair.
type LocalStackProvider struct {
	Endpoint string // e.g. http://localhost:4566
}

// Provider implements ProviderStrategy.
func (p LocalStackProvider) Provider(StackIdentity) ProviderConfig {
	eps := make(map[string]string, len(LocalStackServices))
	for _, s := range LocalStackServices {
		eps[s] = p.Endpoint
	}
	return ProviderConfig{
		Attrs: []Attr{
			{Name: "s3_use_path_style", Value: true},
			{Name: "skip_credentials_validation", Value: true},
			{Name: "skip_metadata_api_check", Value: true},
			{Name: "skip_requesting_account_id", Value: true},
		},
		Endpoints: eps,
	}
}

// AWSProvider targets a real account; credentials come from GitHub OIDC (ADR-0005).
type AWSProvider struct {
	AllowedAccountIDs []string
}

// Provider implements ProviderStrategy.
func (p AWSProvider) Provider(StackIdentity) ProviderConfig {
	return ProviderConfig{Attrs: []Attr{{Name: "allowed_account_ids", Value: p.AllowedAccountIDs}}}
}
