# environments/

**Generated. Do not hand-edit.** One Terraform root per stack:

```
environments/<development|staging|production>/<team>/<stack>/
```

Files are rendered by `backend/internal/generator`. While local-first ([ADR-0009](../docs/adr/0009-local-first-localstack-and-local-backend.md)) applies, stacks use the `local` backend and target LocalStack.
