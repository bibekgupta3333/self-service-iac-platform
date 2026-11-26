from __future__ import annotations

import copy
import shutil
from pathlib import Path

import pytest
import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]
SCHEMA_PATH = REPO_ROOT / "modules" / "_shared" / "module.schema.json"

VALID_MANIFEST = {
    "apiVersion": "iacp.dev/v1",
    "kind": "Module",
    "metadata": {
        "name": "demo",
        "version": "1.0.0",
        "displayName": "Demo",
        "description": "Demo module for tests.",
        "category": "storage",
        "owners": ["@acme/platform"],
        "maturity": "beta",
    },
    "compatibility": {
        "terraform": ">= 1.10, < 2.0",
        "providers": {"hashicorp/aws": ">= 5.60, < 6.0"},
        "environments": ["development"],
    },
    "inputs": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": ["name"],
        "additionalProperties": False,
        "properties": {
            "name": {"type": "string", "x-iacp-immutable": True},
            "size": {"type": "integer", "minimum": 1},
            "tags": {"type": "object", "additionalProperties": {"type": "string"}},
        },
    },
    "outputs": [
        {"name": "id", "description": "Identifier", "wireable": True},
        {"name": "iacp_metadata", "description": "Module name and version"},
    ],
}

VARIABLES_TF = """
variable "name" {
  description = "Name"
  type        = string
}

# variable "commented_out" { type = string }

variable "team" {
  description = "Team"
  type        = string
}

variable "environment" {
  description = "Env { with braces in a string }"
  type        = string
}

variable "stack" {
  description = "Stack"
  type        = string
}

variable "size" {
  description = "Size"
  type        = number
  default     = 1
}

variable "tags" {
  description = "Tags"
  type        = map(string)
  default     = {}
}
"""

OUTPUTS_TF = """
output "id" {
  description = "Identifier"
  value       = "x"
}

output "iacp_metadata" {
  description = "Module identity"
  value       = { module = "demo", version = local.module_version }
}
"""

VERSIONS_TF = """
terraform {
  required_version = ">= 1.10, < 2.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 6.0"
    }
  }
}
"""

LOCALS_TF = """
locals {
  module_version = "1.0.0"
}
"""


@pytest.fixture
def manifest() -> dict:
    return copy.deepcopy(VALID_MANIFEST)


@pytest.fixture
def modules_root(tmp_path: Path) -> Path:
    root = tmp_path / "modules"
    (root / "_shared").mkdir(parents=True)
    shutil.copy(SCHEMA_PATH, root / "_shared" / "module.schema.json")
    return root


@pytest.fixture
def make_module(modules_root: Path):
    def _make(name: str = "demo", manifest: dict | None = None, variables: str = VARIABLES_TF,
              outputs: str = OUTPUTS_TF, locals_tf: str = LOCALS_TF, raw_manifest: str | None = None,
              versions_tf: str | None = VERSIONS_TF) -> Path:
        path = modules_root / name
        path.mkdir()
        if raw_manifest is not None:
            (path / "module.yaml").write_text(raw_manifest)
        elif manifest is not None:
            (path / "module.yaml").write_text(yaml.safe_dump(manifest, sort_keys=False))
        (path / "variables.tf").write_text(variables)
        (path / "outputs.tf").write_text(outputs)
        (path / "locals.tf").write_text(locals_tf)
        if versions_tf is not None:
            (path / "versions.tf").write_text(versions_tf)
        return path

    return _make
