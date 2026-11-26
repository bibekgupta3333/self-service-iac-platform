# tests/

Terratest (Go) integration tests that deploy module examples to LocalStack and assert on real API responses.

| Test | Applies | Asserts through the AWS SDK |
|---|---|---|
| `TestS3Complete` | `modules/s3/examples/complete` | SSE-KMS key, public access block, versioning, TLS-only policy, access logging, required tags, encrypted object round trip |
| `TestSQSComplete` | `modules/sqs/examples/complete` | KMS on both queues, redrive and redrive-allow policies, TLS-only policy, required tags, send/receive |
| `TestDynamoDBComplete` | `modules/dynamodb/examples/complete` | SSE with the module key, point-in-time recovery, on-demand billing, deletion protection off in development, keys, GSI projection, TTL, stream, required tags, put/get item |
| `TestVPCComplete` | `modules/vpc/examples/complete` | CIDR, DNS attributes, three zones of public and private subnets without public IPs, default security group without rules, per-zone NAT routing, REJECT flow log to the module's log group, required tags (with `iacp:tier`) |
| `TestMonitoringComplete` | `modules/monitoring/examples/complete` | Topic KMS key and policy, required tags, email subscription, the four helper alarms notifying the topic, dashboard widgets including the alarm status widget |

```bash
docker compose up -d localstack
cd tests && go test -timeout 30m ./...
```

Tests apply the examples in place (state stays in the git-ignored example
directory) with a random name suffix and destroy everything at the end. Set
`LOCALSTACK_ENDPOINT` to point at a LocalStack other than `http://localhost:4566`.
The SDK clients always set an explicit LocalStack endpoint and dummy
credentials, so they cannot reach real AWS.
