"""Unit tests for the pure parts: parsing, diffing, classification, version decisions, notes."""

from __future__ import annotations

import pytest

from tools.module_release import (
    Bump,
    GateInput,
    InterfaceError,
    SemVer,
    bump_between,
    changelog_section,
    constraint_floor,
    diff_interfaces,
    diff_summary,
    evaluate,
    git_tag,
    is_release_relevant,
    latest_release,
    modules_in_paths,
    parse_interface,
    release_notes,
    required_bump,
)
from tools.module_release.interface import normalise_expression
from tools.module_release.models import Change, Subject
from tools.module_release.versioning import tag_errors

BASE_TF = """
variable "name" {
  description = "Name"
  type        = string
  validation {
    condition     = can(regex("^[a-z]+$", var.name))
    error_message = "lowercase only"
  }
}

# variable "ghost" { type = string }

variable "size" {
  type    = number
  default = 1
}

variable "settings" {
  type = object({
    a = string
    b = optional(number, 3)
  })
  default  = null
}

variable "strict" {
  type     = list(string)
  default  = []
  nullable = false
}

variable "untyped" {
  default = "x"
}

output "id" {
  value = "x"
}
"""


def iface(text: str = BASE_TF):
    return parse_interface({"variables.tf": text})


def edit(old: str, new: str, text: str = BASE_TF) -> str:
    assert old in text, old
    return text.replace(old, new)


class TestSemVer:
    @pytest.mark.parametrize("text", ["1.2.3", "0.0.0", "10.20.30", "1.0.0-rc.1", "1.0.0-alpha.beta+build.5", "1.2.3+meta"])
    def test_parse_valid(self, text):
        assert str(SemVer.parse(text)) == text.split("+")[0]

    @pytest.mark.parametrize("text", ["1.2", "v1.2.3", "01.2.3", "1.2.3-", "1.2.3-01", "", "latest"])
    def test_parse_invalid(self, text):
        assert SemVer.try_parse(text) is None
        with pytest.raises(ValueError):
            SemVer.parse(text)

    def test_ordering_follows_semver_precedence(self):
        ordered = ["1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
                   "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"]
        versions = [SemVer.parse(v) for v in ordered]
        assert sorted(reversed(versions)) == versions
        assert SemVer.parse("1.0.0+a") == SemVer.parse("1.0.0")

    @pytest.mark.parametrize(
        "previous,declared,bump",
        [
            ("1.2.3", "2.0.0", Bump.MAJOR),
            ("1.2.3", "1.3.0", Bump.MINOR),
            ("1.2.3", "1.2.4", Bump.PATCH),
            ("1.2.3", "1.2.3", Bump.NONE),
            ("1.2.3", "1.2.2", Bump.NONE),
            ("1.2.3", "0.9.0", Bump.NONE),
            ("1.2.3", "2.0.0-rc.1", Bump.MAJOR),
        ],
    )
    def test_bump_between(self, previous, declared, bump):
        assert bump_between(SemVer.parse(previous), SemVer.parse(declared)) is bump


class TestTags:
    def test_git_tag(self):
        assert git_tag("s3", SemVer.parse("1.2.0")) == "modules/s3/v1.2.0"

    def test_latest_release_ignores_prereleases_other_modules_and_junk(self):
        tags = ["modules/s3/v1.0.0", "modules/s3/v1.10.0", "modules/s3/v1.9.0", "modules/s3/v2.0.0-rc.1",
                "modules/s3x/v9.0.0", "modules/s3/vbad", "modules/sqs/v3.0.0"]
        assert latest_release("s3", tags) == SemVer.parse("1.10.0")

    def test_latest_release_none(self):
        assert latest_release("s3", ["modules/s3/v1.0.0-rc.1"]) is None
        assert latest_release("s3", []) is None


class TestParseInterface:
    def test_variables_and_outputs(self):
        result = iface()
        assert sorted(result.variables) == ["name", "settings", "size", "strict", "untyped"]
        assert sorted(result.outputs) == ["id"]

    def test_variable_details(self):
        v = iface().variables
        assert v["name"].required and v["name"].type == "string" and len(v["name"].validations) == 1
        assert not v["size"].required and v["size"].default == 1
        assert not v["settings"].required and v["settings"].default is None
        assert v["settings"].type == "object({a=string,b=optional(number,3)})"
        assert v["strict"].nullable is False and v["strict"].default == []
        assert v["untyped"].type == "any"

    def test_formatting_does_not_change_the_interface(self):
        reformatted = edit("type = object({\n    a = string\n    b = optional(number, 3)\n  })",
                           "type = object({ a = string, b = optional(number, 3) })")
        reformatted = edit("can(regex(\"^[a-z]+$\", var.name))", "can( regex(\"^[a-z]+$\",var.name) )", reformatted)
        assert diff_interfaces(iface(), iface(reformatted)) == []

    def test_merges_files_and_ignores_non_tf(self):
        result = parse_interface({"a.tf": 'variable "a" {}', "b.tf": 'output "o" { value = 1 }', "README.md": "#"})
        assert set(result.variables) == {"a"} and set(result.outputs) == {"o"}

    def test_parse_error(self):
        with pytest.raises(InterfaceError, match="broken.tf"):
            parse_interface({"broken.tf": 'variable "a" {'})


def labels(changes):
    return [(c.subject.value, c.name, c.bump) for c in changes]


class TestDiff:
    @pytest.mark.parametrize(
        "old,new,expected",
        [
            pytest.param('variable "size" {\n  type    = number\n  default = 1\n}\n', "",
                         [("variable", "size", Bump.MAJOR)], id="variable-removed"),
            pytest.param('output "id" {', 'output "identifier" {',
                         [("output", "id", Bump.MAJOR), ("output", "identifier", Bump.MINOR)], id="output-renamed"),
            pytest.param("# variable \"ghost\" { type = string }", 'variable "ghost" { type = string }',
                         [("variable", "ghost", Bump.MAJOR)], id="new-required-variable"),
            pytest.param("# variable \"ghost\" { type = string }",
                         'variable "ghost" {\n  type = string\n  default = null\n}',
                         [("variable", "ghost", Bump.MINOR)], id="new-optional-variable"),
            pytest.param("type    = number", "type    = string",
                         [("variable", "size", Bump.MAJOR)], id="type-changed"),
            pytest.param("b = optional(number, 3)", "b = optional(number, 4)",
                         [("variable", "settings", Bump.MAJOR)], id="nested-type-changed"),
            pytest.param("type    = number\n  default = 1", "type    = number",
                         [("variable", "size", Bump.MAJOR)], id="default-removed"),
            pytest.param('description = "Name"\n  type        = string',
                         'description = "Name"\n  type        = string\n  default     = "n"',
                         [("variable", "name", Bump.MINOR)], id="default-added"),
            pytest.param("default = 1", "default = 2", [("variable", "size", Bump.MINOR)], id="default-changed"),
            pytest.param("nullable = false", "nullable = true",
                         [("variable", "strict", Bump.MINOR)], id="nullable-on"),
            pytest.param("default  = []\n  nullable = false", "default  = []",
                         [("variable", "strict", Bump.MINOR)], id="nullable-default-on"),
            pytest.param("type    = number\n  default = 1",
                         "type    = number\n  default = 1\n  nullable = false",
                         [("variable", "size", Bump.MAJOR)], id="nullable-off"),
            pytest.param('"^[a-z]+$"', '"^[a-z0-9]+$"', [("variable", "name", Bump.MAJOR)], id="validation-changed"),
            pytest.param("type    = number\n  default = 1",
                         "type    = number\n  default = 1\n  validation {\n    condition = var.size > 0\n"
                         "    error_message = \"positive\"\n  }",
                         [("variable", "size", Bump.MAJOR)], id="validation-added"),
            pytest.param("  validation {\n    condition     = can(regex(\"^[a-z]+$\", var.name))\n"
                         "    error_message = \"lowercase only\"\n  }\n", "",
                         [("variable", "name", Bump.MINOR)], id="validation-removed"),
            pytest.param('error_message = "lowercase only"', 'error_message = "use lowercase letters"', [],
                         id="error-message-only"),
            pytest.param('description = "Name"', 'description = "The name"', [], id="description-only"),
            pytest.param('output "id" {\n  value = "x"\n}', 'output "id" {\n  value = "x"\n}\noutput "arn" {\n  value = "y"\n}',
                         [("output", "arn", Bump.MINOR)], id="output-added"),
            pytest.param('output "id" {\n  value = "x"\n}', 'output "id" {\n  value     = "x"\n  sensitive = true\n}',
                         [("output", "id", Bump.MAJOR)], id="output-marked-sensitive"),
            pytest.param('output "id" {\n  value = "x"\n}', 'output "id" {\n  value     = "x"\n  sensitive = false\n}',
                         [], id="output-sensitive-false-is-default"),
        ],
    )
    def test_classification(self, old, new, expected):
        assert labels(diff_interfaces(iface(), iface(edit(old, new)))) == expected

    def test_type_change_and_default_removed_are_both_reported(self):
        changes = diff_interfaces(iface(), iface(edit("type    = number\n  default = 1", "type    = string")))
        assert [c.description.split(" ")[0] for c in changes] == ["type", "default"]

    def test_change_render(self):
        change = Change(Subject.OUTPUT, "id", "removed (or renamed)", Bump.MAJOR)
        assert change.render() == "[major] output 'id': removed (or renamed)"

    @pytest.mark.parametrize(
        "bumps,content_changed,expected",
        [
            ([], False, Bump.NONE),
            ([], True, Bump.PATCH),
            ([Bump.MINOR], False, Bump.MINOR),
            ([Bump.MINOR, Bump.MAJOR, Bump.MINOR], True, Bump.MAJOR),
        ],
    )
    def test_required_bump(self, bumps, content_changed, expected):
        changes = [Change(Subject.VARIABLE, f"v{i}", "x", b) for i, b in enumerate(bumps)]
        assert required_bump(changes, content_changed) is expected

    @pytest.mark.parametrize(
        "path,relevant",
        [("main.tf", True), ("README.md", True), ("tests/basic.tftest.hcl", False),
         ("examples/complete/main.tf", False), ("testsuite.tf", True)],
    )
    def test_is_release_relevant(self, path, relevant):
        assert is_release_relevant(path) is relevant

    def test_modules_in_paths(self):
        paths = ["modules/s3/main.tf", "modules/s3/tests/a.hcl", "modules/sqs/README.md", "modules/README.md",
                 "modules/_shared/module.schema.json", "backend/go.mod", "modules/lambda/examples/x/main.tf"]
        assert modules_in_paths(paths) == ["lambda", "s3", "sqs"]


def gate(declared: str, previous: str | None, bumps=(), content_changed=None):
    changes = tuple(Change(Subject.VARIABLE, f"v{i}", "x", b) for i, b in enumerate(bumps))
    changed = bool(bumps) if content_changed is None else content_changed
    return evaluate(GateInput("demo", SemVer.parse(declared), SemVer.parse(previous) if previous else None,
                              changes, changed))


class TestEvaluate:
    def test_first_release(self):
        result = gate("1.0.0", None)
        assert result.ok and result.first_release and result.releasable
        assert result.tag == "modules/demo/v1.0.0"

    @pytest.mark.parametrize(
        "declared,previous,bumps,ok",
        [
            ("2.0.0", "1.4.2", [Bump.MAJOR], True),
            ("1.5.0", "1.4.2", [Bump.MAJOR], False),
            ("1.4.3", "1.4.2", [Bump.MAJOR], False),
            ("1.5.0", "1.4.2", [Bump.MINOR], True),
            ("1.4.3", "1.4.2", [Bump.MINOR], False),
            ("2.0.0", "1.4.2", [Bump.MINOR], True),
            ("1.4.3", "1.4.2", [], True),
            ("2.0.0-rc.1", "1.4.2", [Bump.MAJOR], True),
            ("1.5.0-rc.1", "1.4.2", [Bump.MAJOR], False),
        ],
    )
    def test_bump_size(self, declared, previous, bumps, ok):
        assert gate(declared, previous, bumps, content_changed=True).ok is ok

    def test_minor_for_breaking_change_explains_requirement(self):
        result = gate("1.5.0", "1.4.2", [Bump.MAJOR])
        assert result.required is Bump.MAJOR and result.actual is Bump.MINOR
        assert "require at least a major bump" in result.errors[0]

    def test_unbumped_change_fails(self):
        result = gate("1.4.2", "1.4.2", content_changed=True)
        assert not result.ok and "was not bumped" in result.errors[0] and "patch" in result.errors[0]

    def test_unbumped_interface_change_names_required_bump(self):
        assert "major" in gate("1.4.2", "1.4.2", [Bump.MAJOR]).errors[0]

    def test_unchanged_and_unbumped_is_ok_but_not_releasable(self):
        result = gate("1.4.2", "1.4.2", content_changed=False)
        assert result.ok and not result.releasable

    def test_downgrade_fails(self):
        assert "lower than the last release" in gate("1.4.1", "1.4.2").errors[0]
        assert not gate("1.4.2-rc.1", "1.4.2").ok

    def test_zero_major_relaxes_breaking_to_minor(self):
        result = gate("0.3.0", "0.2.5", [Bump.MAJOR])
        assert result.ok and result.required is Bump.MINOR
        assert any("0.x" in w for w in result.warnings)
        assert not gate("0.2.6", "0.2.5", [Bump.MAJOR]).ok

    def test_skipped_version_warns(self):
        result = gate("2.1.0", "1.4.2", [Bump.MAJOR])
        assert result.ok and any("skips versions" in w and "2.0.0" in w for w in result.warnings)

    def test_bump_without_changes_warns(self):
        result = gate("1.4.3", "1.4.2", content_changed=False)
        assert result.ok and any("nothing release-relevant" in w for w in result.warnings)

    def test_extra_errors_fail(self):
        result = evaluate(GateInput("demo", SemVer.parse("1.0.1"), SemVer.parse("1.0.0"), (), True, ("parse error",)))
        assert not result.ok and result.errors == ("parse error",)

    def test_to_dict(self):
        data = gate("2.0.0", "1.0.0", [Bump.MAJOR]).to_dict()
        assert data["required_bump"] == "major" and data["actual_bump"] == "major" and data["ok"]
        assert data["changes"][0] == {"subject": "variable", "name": "v0", "description": "x", "bump": "major"}


CHANGELOG = """# Changelog

## [Unreleased]
- nothing yet

## [1.1.0] - 2026-12-01

### Added
- `retention_days` input.

## [1.0.0] - 2026-11-25

### Added
- Initial release.
"""


class TestNotes:
    def test_section_found(self):
        assert changelog_section(CHANGELOG, SemVer.parse("1.1.0")) == "### Added\n- `retention_days` input."

    def test_last_section_runs_to_end(self):
        assert changelog_section(CHANGELOG, SemVer.parse("1.0.0")) == "### Added\n- Initial release."

    @pytest.mark.parametrize("heading", ["## 1.2.0", "## [v1.2.0]", "## v1.2.0 (2026-12-02)"])
    def test_heading_variants(self, heading):
        assert changelog_section(f"{heading}\nbody\n", SemVer.parse("1.2.0")) == "body"

    def test_missing_section_or_file(self):
        assert changelog_section(CHANGELOG, SemVer.parse("9.9.9")) is None
        assert changelog_section(None, SemVer.parse("1.0.0")) is None
        assert changelog_section("## [1.0.0]\n\n## [0.9.0]\nx", SemVer.parse("1.0.0")) is None

    def test_summary_lists_changes(self):
        text = diff_summary(gate("2.0.0", "1.1.0", [Bump.MAJOR]))
        assert "Previous release: 1.1.0" in text and "[major] variable 'v0'" in text

    def test_summary_first_release_and_no_changes(self):
        assert "First release" in diff_summary(gate("1.0.0", None))
        assert "No interface changes" in diff_summary(gate("1.0.1", "1.0.0", content_changed=True))

    def test_release_notes_prefers_changelog(self):
        assert release_notes(CHANGELOG, gate("1.1.0", "1.0.0", [Bump.MINOR])).startswith("### Added")
        assert release_notes(CHANGELOG, gate("1.2.0", "1.1.0", [Bump.MINOR])).startswith("Release of `demo`")


class TestOutputSensitivity:
    SENSITIVE = BASE_TF.replace('output "id" {\n  value = "x"\n}', 'output "id" {\n  value     = "x"\n  sensitive = true\n}')

    def test_parsed(self):
        assert iface(self.SENSITIVE).outputs["id"].sensitive is True
        assert iface().outputs["id"].sensitive is False

    def test_turning_off_is_minor(self):
        changes = diff_interfaces(iface(self.SENSITIVE), iface())
        assert labels(changes) == [("output", "id", Bump.MINOR)]
        assert "no longer sensitive" in changes[0].description


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


def versions(text: str = VERSIONS_TF):
    return parse_interface({"variables.tf": BASE_TF, "versions.tf": text})


class TestRequirements:
    def test_parsed(self):
        result = versions()
        assert result.terraform_version == ">= 1.10, < 2.0"
        assert result.providers == {"hashicorp/aws": ">= 5.60, < 6.0"}

    def test_legacy_shorthand_and_missing_version(self):
        result = parse_interface({"v.tf": 'terraform {\n  required_providers {\n    aws = "~> 5.0"\n'
                                          '    random = { source = "hashicorp/random" }\n  }\n}\n'})
        assert result.providers == {"aws": "~> 5.0", "hashicorp/random": None}
        assert result.terraform_version is None

    @pytest.mark.parametrize(
        "constraint,floor",
        [
            (None, (0, 0, 0)),
            ("", (0, 0, 0)),
            ("< 6.0", (0, 0, 0)),
            (">= 5.60, < 6.0", (5, 60, 0)),
            ("~> 5.6", (5, 6, 0)),
            ("~> 5.6.1", (5, 6, 1)),
            ("5.2.0", (5, 2, 0)),
            ("= 5.2.0", (5, 2, 0)),
            ("> 4.9", (4, 9, 0)),
            (">= 4.0, >= 5.1, != 5.3.0", (5, 1, 0)),
            (">= 1.10.0-beta1", (1, 10, 0)),
            ("whatever", None),
        ],
    )
    def test_constraint_floor(self, constraint, floor):
        assert constraint_floor(constraint) == floor

    @pytest.mark.parametrize(
        "old,new,expected",
        [
            pytest.param('">= 5.60, < 6.0"', '">= 6.0, < 7.0"', [("provider", "hashicorp/aws", Bump.MAJOR)],
                         id="provider-major-floor-raised"),
            pytest.param('">= 5.60, < 6.0"', '">= 5.70, < 6.0"', [("provider", "hashicorp/aws", Bump.MINOR)],
                         id="provider-minor-floor-raised"),
            pytest.param('">= 5.60, < 6.0"', '">= 5.60, < 7.0"', [], id="provider-ceiling-raised"),
            pytest.param('">= 5.60, < 6.0"', '">= 5.0, < 6.0"', [], id="provider-floor-lowered"),
            pytest.param('">= 5.60, < 6.0"', '"~> 5.60"', [], id="provider-same-floor-other-syntax"),
            pytest.param('">= 5.60, < 6.0"', '"latest"', [("provider", "hashicorp/aws", Bump.MAJOR)],
                         id="provider-unreadable"),
            pytest.param('">= 1.10, < 2.0"', '">= 2.0"', [("terraform", "required_version", Bump.MAJOR)],
                         id="terraform-major-floor-raised"),
            pytest.param('">= 1.10, < 2.0"', '">= 1.11, < 2.0"', [("terraform", "required_version", Bump.MINOR)],
                         id="terraform-minor-floor-raised"),
        ],
    )
    def test_classification(self, old, new, expected):
        assert labels(diff_interfaces(versions(), versions(VERSIONS_TF.replace(old, new)))) == expected

    def test_new_provider_is_minor(self):
        added = VERSIONS_TF.replace("    aws = {", '    random = {\n      source  = "hashicorp/random"\n'
                                                  '      version = ">= 3.0"\n    }\n    aws = {')
        assert labels(diff_interfaces(versions(), versions(added))) == [("provider", "hashicorp/random", Bump.MINOR)]

    def test_adding_a_terraform_floor_from_nothing(self):
        before = parse_interface({"variables.tf": BASE_TF})
        assert labels(diff_interfaces(before, versions())) == [
            ("terraform", "required_version", Bump.MAJOR), ("provider", "hashicorp/aws", Bump.MINOR)]


class TestNormalisation:
    @pytest.mark.parametrize(
        "raw,expected",
        [
            ('${contains(["a b"], var.x)}', 'contains(["a b"],var.x)'),
            ('${length( var.x ) > 0}', "length(var.x)>0"),
            ('${var.x == "say \\"hi there\\""}', 'var.x=="say \\"hi there\\""'),
            ("map( string )", "map(string)"),
        ],
    )
    def test_keeps_string_literals(self, raw, expected):
        assert normalise_expression(raw) == expected

    def test_whitespace_inside_a_literal_is_a_validation_change(self):
        text = BASE_TF.replace('can(regex("^[a-z]+$", var.name))', 'contains(["a b"], var.name)')
        narrowed = text.replace('["a b"]', '["ab"]')
        assert labels(diff_interfaces(iface(text), iface(narrowed))) == [("variable", "name", Bump.MAJOR)]


class TestTagErrors:
    V = SemVer.parse

    def test_reused_tag_with_changes(self):
        errors = tag_errors(self.V("2.0.0-rc.1"), [self.V("1.0.0"), self.V("2.0.0-rc.1")], True, self.V("1.0.0"))
        assert len(errors) == 1 and "already exists" in errors[0]

    def test_reused_tag_without_changes(self):
        assert tag_errors(self.V("2.0.0-rc.1"), [self.V("2.0.0-rc.1")], False, None) == []

    def test_equal_final_is_left_to_the_bump_check(self):
        assert tag_errors(self.V("1.0.0"), [self.V("1.0.0")], True, self.V("1.0.0")) == []

    @pytest.mark.parametrize(
        "declared,tagged,lower",
        [
            ("2.0.0-rc.0", ["2.0.0-rc.1"], True),
            ("2.0.0-rc.2", ["2.0.0-rc.1"], False),
            ("2.0.0-beta.9", ["2.0.0-rc.1"], True),
            ("2.1.0-rc.0", ["2.0.0-rc.1"], False),
        ],
    )
    def test_lower_prerelease(self, declared, tagged, lower):
        errors = tag_errors(self.V(declared), [self.V(t) for t in tagged], False, None)
        assert bool(errors) is lower

    def test_evaluate_includes_tag_errors(self):
        result = evaluate(GateInput("demo", SemVer.parse("2.0.0-rc.1"), SemVer.parse("1.0.0"),
                                    (Change(Subject.VARIABLE, "x", "x", Bump.MAJOR),), True,
                                    tagged=(SemVer.parse("2.0.0-rc.1"),), changed_since_declared=True))
        assert not result.ok
