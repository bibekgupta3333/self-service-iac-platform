import json

import pytest

from tools.manifest_validator import (
    ManifestLoader,
    ManifestValidator,
    OutputParityChecker,
    SchemaValidator,
    VariableParityChecker,
    VersionStampChecker,
)
from tools.manifest_validator.cli import main

from .conftest import REPO_ROOT, SCHEMA_PATH, VARIABLES_TF


def codes(report):
    return sorted({f.code for f in report.findings})


def messages(report):
    return "\n".join(f.message for f in report.findings)


class TestSchemaValidator:
    @pytest.fixture
    def validator(self):
        return SchemaValidator.from_file(SCHEMA_PATH)

    def _errors(self, validator, manifest, tmp_path):
        from tools.manifest_validator.models import ModulePackage

        return validator.check(ModulePackage(name="demo", path=tmp_path, manifest=manifest))

    def test_valid_manifest(self, validator, manifest, tmp_path):
        assert self._errors(validator, manifest, tmp_path) == []

    @pytest.mark.parametrize(
        "mutate,fragment",
        [
            (lambda m: m.update(apiVersion="iacp.dev/v2"), "apiVersion"),
            (lambda m: m["metadata"].update(version="1.0"), "metadata.version"),
            (lambda m: m["metadata"].update(category="banana"), "metadata.category"),
            (lambda m: m["metadata"].update(description="x" * 201), "metadata.description"),
            (lambda m: m["metadata"].update(owners=["platform"]), "metadata.owners[0]"),
            (lambda m: m["compatibility"].update(environments=["prod"]), "compatibility.environments[0]"),
            (lambda m: m["inputs"]["properties"]["name"].update({"x-iacp-unknown": 1}), "inputs.properties.name"),
            (lambda m: m["inputs"]["properties"]["name"].pop("type"), "inputs.properties.name"),
            (lambda m: m["outputs"].append({"name": "Bad-Name", "description": "x"}), "outputs[2].name"),
            (lambda m: m.pop("outputs"), "<root>"),
            (lambda m: m.update(extra=True), "<root>"),
        ],
    )
    def test_invalid_manifest(self, validator, manifest, tmp_path, mutate, fragment):
        mutate(manifest)
        errors = self._errors(validator, manifest, tmp_path)
        assert errors, "expected schema errors"
        assert any(e.message.startswith(fragment) for e in errors), [e.message for e in errors]


class TestManifestValidator:
    def test_clean_module_passes(self, make_module, manifest, modules_root):
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert report.ok, report.render_text()
        assert report.modules == ["demo"]

    def test_shared_and_hidden_dirs_skipped(self, make_module, manifest, modules_root):
        (modules_root / ".cache").mkdir()
        make_module(manifest=manifest)
        assert ManifestValidator.for_root(modules_root).run().modules == ["demo"]

    def test_missing_manifest(self, make_module, modules_root):
        make_module()
        report = ManifestValidator.for_root(modules_root).run()
        assert codes(report) == ["manifest"]
        assert "module.yaml not found" in messages(report)

    def test_invalid_yaml(self, make_module, modules_root):
        make_module(raw_manifest="a: [unclosed")
        report = ManifestValidator.for_root(modules_root).run()
        assert "invalid YAML" in messages(report)

    def test_name_mismatch(self, make_module, manifest, modules_root):
        make_module(name="other", manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert codes(report) == ["metadata"]

    def test_input_missing_from_variables(self, make_module, manifest, modules_root):
        manifest["inputs"]["properties"]["extra"] = {"type": "string"}
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert codes(report) == ["input-parity"]
        assert "inputs missing from variables.tf: 'extra'" in messages(report)

    def test_variable_missing_from_manifest(self, make_module, manifest, modules_root):
        del manifest["inputs"]["properties"]["size"]
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert "variables missing from module.yaml inputs: 'size'" in messages(report)

    def test_injected_variables_not_required_in_manifest(self, make_module, manifest, modules_root):
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert "team" not in messages(report)

    def test_injected_variables_must_exist(self, make_module, manifest, modules_root):
        make_module(manifest=manifest, variables=VARIABLES_TF.replace('variable "stack"', 'variable "stack_name"'))
        report = ManifestValidator.for_root(modules_root).run()
        assert "platform-injected variables missing: 'stack'" in messages(report)

    def test_type_mismatch(self, make_module, manifest, modules_root):
        manifest["inputs"]["properties"]["size"]["type"] = "string"
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert "input 'size'" in messages(report)

    def test_output_parity(self, make_module, manifest, modules_root):
        manifest["outputs"].append({"name": "arn", "description": "ARN"})
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert codes(report) == ["output-parity"]
        assert "outputs missing from outputs.tf: 'arn'" in messages(report)

    def test_output_not_in_manifest(self, make_module, manifest, modules_root):
        manifest["outputs"] = [o for o in manifest["outputs"] if o["name"] != "id"]
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert "outputs missing from module.yaml: 'id'" in messages(report)

    def test_version_stamp_mismatch(self, make_module, manifest, modules_root):
        make_module(manifest=manifest, locals_tf='locals {\n  module_version = "0.9.0"\n}\n')
        report = ManifestValidator.for_root(modules_root).run()
        assert codes(report) == ["version-stamp"]

    def test_version_stamp_missing(self, make_module, manifest, modules_root):
        make_module(manifest=manifest, locals_tf="")
        report = ManifestValidator.for_root(modules_root).run()
        assert "locals.module_version is not set" in messages(report)

    def test_default_violating_its_pattern(self, make_module, manifest, modules_root):
        manifest["inputs"]["properties"]["name"].update({"pattern": "^[a-z]+$", "default": "Bad_Name"})
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert codes(report) == ["inputs-schema"]
        assert "input 'name': default 'Bad_Name' is invalid" in messages(report)

    def test_default_violating_property_names(self, make_module, manifest, modules_root):
        manifest["inputs"]["properties"]["tags"].update(
            {"propertyNames": {"not": {"pattern": "^iacp:"}}, "default": {"iacp:team": "x"}}
        )
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert "input 'tags'" in messages(report)

    def test_valid_defaults_pass(self, make_module, manifest, modules_root):
        manifest["inputs"]["properties"]["size"]["default"] = 3
        make_module(manifest=manifest)
        assert ManifestValidator.for_root(modules_root).run().ok

    def test_inputs_not_a_valid_schema(self, make_module, manifest, modules_root):
        manifest["inputs"]["properties"]["size"]["minimum"] = "one"
        make_module(manifest=manifest)
        report = ManifestValidator.for_root(modules_root).run()
        assert "inputs is not a valid JSON Schema" in messages(report)

    def test_checks_are_pluggable(self, make_module, manifest, modules_root):
        manifest["outputs"].append({"name": "arn", "description": "ARN"})
        make_module(manifest=manifest)
        validator = ManifestValidator(ManifestLoader(modules_root), [VariableParityChecker(), VersionStampChecker()])
        assert validator.run().ok
        validator = ManifestValidator(ManifestLoader(modules_root), [OutputParityChecker()])
        assert not validator.run().ok

    def test_missing_root_raises(self, tmp_path):
        with pytest.raises(FileNotFoundError):
            ManifestLoader(tmp_path / "nope").discover()


class TestCli:
    def test_exit_zero_when_clean(self, make_module, manifest, modules_root, capsys):
        make_module(manifest=manifest)
        assert main([str(modules_root)]) == 0
        assert "1 module(s) checked, 0 error(s)" in capsys.readouterr().out

    def test_exit_one_with_errors_json(self, make_module, manifest, modules_root, capsys):
        manifest["metadata"]["maturity"] = "ga"
        make_module(manifest=manifest)
        assert main([str(modules_root), "--format", "json"]) == 1
        payload = json.loads(capsys.readouterr().out)
        assert payload["ok"] is False
        assert payload["findings"][0]["code"] == "schema"

    def test_exit_two_when_root_missing(self, tmp_path, capsys):
        assert main([str(tmp_path / "missing"), "--schema", str(SCHEMA_PATH)]) == 2
        assert "error:" in capsys.readouterr().err


def test_repository_modules_are_valid():
    """The real modules/ tree must always pass (guards against drift in this repo)."""
    report = ManifestValidator.for_root(REPO_ROOT / "modules").run()
    assert report.ok, report.render_text()
