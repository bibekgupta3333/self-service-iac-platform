# backend/

Go control plane: REST API, worker, and config generator.

```
cmd/api        HTTP API entrypoint
cmd/worker     queue consumer (status updates, TTL, drift)
internal/      domain, services, repositories, generator, github, httpapi
migrations/    PostgreSQL schema migrations
```

See [docs/01-system-architecture.md](../docs/01-system-architecture.md) and [docs/03-data-model-and-api.md](../docs/03-data-model-and-api.md).
