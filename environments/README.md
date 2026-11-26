# environments/

**Generated. Do not hand-edit.** One Terraform root per stack:

```
environments/<development|staging|production>/<team>/<stack>/
```

Files are rendered by `backend/internal/generator`. While local-first ([ADR-0009](../docs/adr/0009-local-first-localstack-and-local-backend.md)) applies, stacks use the `local` backend and target LocalStack.

`development/platform/smoke/` is the smoke stack (WBS 2.6). It was rendered by the generator with the LocalStack provider and local backend, and `make stack-plan` runs it through the same checks as `pr-validate.yml`.
