"""Discovery and loading of module packages from disk."""

from __future__ import annotations

from pathlib import Path

import yaml

from . import hcl
from .models import ModulePackage

MANIFEST_NAME = "module.yaml"


class ManifestLoader:
    """Finds ``modules/<name>/`` directories and loads their manifest and HCL."""

    def __init__(self, modules_root: Path) -> None:
        self.modules_root = Path(modules_root)

    def discover(self) -> list[ModulePackage]:
        """Load every module directory. Directories starting with ``_`` or ``.`` are skipped."""
        if not self.modules_root.is_dir():
            raise FileNotFoundError(f"modules directory not found: {self.modules_root}")
        return [
            self.load(path)
            for path in sorted(self.modules_root.iterdir())
            if path.is_dir() and not path.name.startswith(("_", "."))
        ]

    def load(self, path: Path) -> ModulePackage:
        package = ModulePackage(name=path.name, path=path)
        package.manifest, package.load_error = self._read_manifest(path / MANIFEST_NAME)
        tf_text = hcl.read_tf_files(str(p) for p in sorted(path.glob("*.tf")))
        package.variables = {
            name: hcl.type_family(body) for name, body in hcl.find_blocks(tf_text, "variable").items()
        }
        package.outputs = hcl.find_blocks(tf_text, "output")
        package.locals_body = hcl.find_locals(tf_text)
        return package

    @staticmethod
    def _read_manifest(path: Path) -> tuple[dict | None, str | None]:
        if not path.is_file():
            return None, f"{MANIFEST_NAME} not found"
        try:
            data = yaml.safe_load(path.read_text(encoding="utf-8"))
        except yaml.YAMLError as exc:
            return None, f"invalid YAML: {exc}"
        if not isinstance(data, dict):
            return None, f"{MANIFEST_NAME} must be a mapping"
        return data, None
