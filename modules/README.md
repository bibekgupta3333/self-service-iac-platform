# modules/

Reusable Terraform packages (the registry). Each module has:

```
modules/<name>/
├── module.yaml        # manifest: metadata, inputs schema, outputs, owners
├── main.tf variables.tf outputs.tf versions.tf
├── README.md
├── examples/complete/ # runnable example (LocalStack)
└── tests/             # terraform test files
```

Versioned by git tags `modules/<name>/vX.Y.Z`. See [docs/04-module-registry.md](../docs/04-module-registry.md).
