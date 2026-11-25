# tests/

Terratest (Go) integration tests that deploy module examples to LocalStack and assert on real API responses.

| Test | Applies | Asserts through the AWS SDK |
|---|---|---|
| `TestS3Complete` | `modules/s3/examples/complete` | SSE-KMS key, public access block, versioning, TLS-only policy, access logging, required tags, encrypted object round trip |
| `TestSQSComplete` | `modules/sqs/examples/complete` | KMS on both queues, redrive and redrive-allow policies, TLS-only policy, required tags, send/receive |

```bash
docker compose up -d localstack
cd tests && go test -timeout 30m ./...
```

Tests apply the examples in place (state stays in the git-ignored example
directory) with a random name suffix and destroy everything at the end. Set
`LOCALSTACK_ENDPOINT` to point at a LocalStack other than `http://localhost:4566`.
The SDK clients always set an explicit LocalStack endpoint and dummy
credentials, so they cannot reach real AWS.
