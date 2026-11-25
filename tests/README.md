# tests/

Terratest (Go) integration tests that deploy module examples to LocalStack and assert on real API responses.

```bash
docker compose up -d localstack
cd tests && go test -timeout 30m ./...
```
