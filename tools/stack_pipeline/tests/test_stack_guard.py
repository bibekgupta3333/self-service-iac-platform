"""The static stack-root check that runs before init and plan (stack_guard.py)."""

import os
from pathlib import Path

import pytest

from tools.stack_pipeline.cli import main
from tools.stack_pipeline.stack_guard import (
    HclScanError,
    check_lock,
    check_stack,
    check_tf,
    parse_blocks,
    provider_sources,
)

SRC = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/s3?ref=modules/s3/v1.0.0"

GOOD = {
    "main.tf": f'module "bucket" {{\n  source = "{SRC}"\n  name   = "x"\n  tags   = {{ team = "${{var.team}}" }}\n}}\n',
    "versions.tf": 'terraform {\n  required_version = "~> 1.13"\n  required_providers {\n    aws = {\n      source  = "hashicorp/aws"\n'
                   '      version = "~> 5.80"\n    }\n  }\n}\n',
    "backend.tf": 'terraform {\n  backend "local" {\n    path = "terraform.tfstate"\n  }\n}\n',
    "providers.tf": 'provider "aws" {\n  region = "us-east-1"\n  endpoints {\n    s3 = "http://localhost:4566"\n  }\n'
                    '  default_tags {\n    tags = { "iacp:team" = "x" }\n  }\n}\n',
    "variables.tf": 'variable "team" {\n  type = string\n  default = "x"\n  validation {\n    condition = length(var.team) > 1\n'
                    '    error_message = "short"\n  }\n}\n',
    "outputs.tf": 'output "arn" {\n  value = module.bucket.arn\n}\nlocals {\n  a = { b = 1 }\n}\n',
    ".terraform.lock.hcl": 'provider "registry.terraform.io/hashicorp/aws" {\n  version = "5.80.0"\n  hashes = [\n    "h1:abc",\n  ]\n}\n',
}


def rules(files):
    return sorted({v.rule for v in check_stack(files)})


class TestParseBlocks:
    def test_blocks_labels_depth_and_parent(self):
        blocks = parse_blocks('terraform {\n  backend "local" {\n    path = "x"\n  }\n}\n')
        assert [(b.kind, b.labels, b.depth, b.parent) for b in blocks] == [
            ("terraform", (), 0, None), ("backend", ("local",), 1, "terraform")]
        assert 'path = "x"' in blocks[1].body

    def test_object_values_are_not_blocks(self):
        assert [b.kind for b in parse_blocks('locals {\n  a = { b = { c = 1 } }\n  d = merge({ e = 1 }, {\n f = 2 })\n}\n')] == ["locals"]

    def test_braces_in_strings_comments_and_templates_do_not_count(self):
        text = ('# resource "x" "y" {\n// data "external" "e" {\n/* provisioner "local-exec" { */\n'
                'locals {\n  a = "}{ resource \\" {"\n  b = "${join(",", ["}", "{"])}"\n  c = "$${ no }"\n}\n')
        assert [b.kind for b in parse_blocks(text)] == ["locals"]

    def test_heredocs_are_skipped(self):
        text = 'locals {\n  doc = <<-EOT\n    resource "x" "y" {\n  EOT\n}\nmodule "m" {\n}\n'
        assert [b.kind for b in parse_blocks(text)] == ["locals", "module"]

    @pytest.mark.parametrize("text", ['locals {\n a = "x\n}', "locals {\n", "}", "/* open", 'x = <<EOT\nnever closed\n'])
    def test_unscannable_text_raises(self, text):
        with pytest.raises(HclScanError):
            parse_blocks(text)

    def test_a_block_header_spread_over_a_line_break_in_parens_is_not_a_block(self):
        assert parse_blocks('locals {\n  a = f(\n    x,\n    { b = 1 }\n  )\n}\n')[0].kind == "locals"


class TestProviderSources:
    def test_sources_and_implied_sources(self):
        body = '\n  aws = {\n    source = "hashicorp/aws"\n    version = "1"\n  }\n  random = ">= 3"\n  ext = { source = "registry.terraform.io/hashicorp/external" }\n'
        assert provider_sources(body) == {"aws": "hashicorp/aws", "random": "hashicorp/random", "ext": "hashicorp/external"}


class TestCheckStack:
    def test_a_generated_stack_passes(self):
        assert check_stack(GOOD) == []

    @pytest.mark.parametrize("snippet,rule", [
        ('data "external" "e" {\n  program = ["sh", "-c", "id"]\n}\n', "block"),
        ('data "http" "h" {\n  url = "https://example.com"\n}\n', "block"),
        ('resource "aws_s3_bucket" "b" {\n}\n', "block"),
        ('resource "null_resource" "n" {\n  provisioner "local-exec" {\n    command = "id"\n  }\n}\n', "block"),
        ('import {\n  to = x.y\n  id = "z"\n}\n', "block"),
        ('check "c" {\n}\n', "block"),
        ('removed {\n  from = x.y\n}\n', "block"),
        ('provider "external" {\n}\n', "provider"),
        ('provider "aws" {\n  alias = "x"\n}\nprovider {\n}\n', "provider"),
        ('terraform {\n  required_providers {\n    external = {\n      source = "hashicorp/external"\n    }\n  }\n}\n', "provider"),
        ('terraform {\n  required_providers {\n    evil = {\n      source = "evil.example/x/evil"\n    }\n  }\n}\n', "provider"),
        ('terraform {\n  required_providers {\n    http = ">= 1"\n  }\n}\n', "provider"),
        ('terraform {\n  backend "http" {\n  }\n}\n', "backend"),
        ('terraform {\n  cloud {\n  }\n}\n', "backend"),
        ('module "m" {\n  source = "./local"\n}\n', "module"),
        ('module "m" {\n  source = "../../modules/s3"\n}\n', "module"),
        ('module "m" {\n  source = "git::https://github.com/evil/x.git//modules/s3?ref=modules/s3/v1.0.0"\n}\n', "module"),
        ('module "m" {\n  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/s3?ref=main"\n}\n', "module"),
        ('module "m" {\n  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/s3?ref=modules/sqs/v1.0.0"\n}\n', "module"),
        ('module "m" {\n  name = "no source"\n}\n', "module"),
        ('locals {\n  x = 1\n  connection {\n  }\n}\n', "provisioner"),
    ])
    def test_refused(self, snippet, rule):
        got = check_stack({"extra.tf": snippet})
        assert rule in {v.rule for v in got}, got

    def test_a_violation_names_the_file_line_and_fix(self):
        (v,) = check_stack({"x.tf": '\n\ndata "external" "e" {\n}\n'})
        assert (v.path, v.line, v.rule) == ("x.tf", 3, "block")
        assert v.render().startswith("x.tf:3: [block] data \"external\" \"e\"") and "platform module" in v.render()

    def test_an_unscannable_file_is_refused_not_skipped(self):
        assert rules({"x.tf": 'module "m" {'}) == ["unreadable"]

    def test_json_configuration_is_refused(self):
        assert rules({"extra.tf.json": "{}"}) == ["json"]

    def test_other_files_are_ignored(self):
        assert check_stack({"iacp.stack.json": "{}", "notes.md": 'data "external" "x" {', "terraform.tfvars": "a = 1"}) == []

    def test_every_problem_is_listed(self):
        text = 'data "http" "h" {\n}\nresource "x" "y" {\n}\nprovider "external" {\n}\n'
        assert [v.rule for v in check_tf("x.tf", text)] == ["block", "block", "provider"]

    def test_a_different_repository_can_be_configured(self):
        src = "git::https://github.com/acme/mods.git//modules/s3?ref=modules/s3/v1.0.0"
        assert check_tf("m.tf", f'module "m" {{\n  source = "{src}"\n}}\n', repo_url="https://github.com/acme/mods.git") == []
        assert check_tf("m.tf", f'module "m" {{\n  source = "{src}"\n}}\n') != []

    def test_prerelease_pins_are_allowed(self):
        src = SRC.replace("v1.0.0", "v1.0.0-rc.1")
        assert check_tf("m.tf", f'module "m" {{\n  source = "{src}"\n}}\n') == []


class TestCheckLock:
    def test_allowed_providers(self):
        assert check_lock("l", GOOD[".terraform.lock.hcl"]) == []
        assert check_lock("l", 'provider "registry.terraform.io/hashicorp/random" {\n}\n') == []

    @pytest.mark.parametrize("address", ["registry.terraform.io/hashicorp/external", "registry.terraform.io/hashicorp/http",
                                         "evil.example/x/aws", "registry.terraform.io/evil/aws"])
    def test_other_providers_are_refused(self, address):
        assert [v.rule for v in check_lock("l", f'provider "{address}" {{\n  version = "1"\n}}\n')] == ["provider"]

    def test_other_blocks_and_unscannable_text_are_refused(self):
        assert check_lock("l", 'resource "a" "b" {\n}\n') != []
        assert [v.rule for v in check_lock("l", 'provider "x" {')] == ["unreadable"]

    def test_the_lock_file_is_scanned_through_check_stack(self):
        files = {**GOOD, ".terraform.lock.hcl": 'provider "registry.terraform.io/hashicorp/external" {\n}\n'}
        assert rules(files) == ["provider"]


class TestCommand:
    def write(self, tmp_path, files):
        root = tmp_path / "environments" / "staging" / "payments" / "orders"
        root.mkdir(parents=True)
        for name, text in files.items():
            (root / name).write_text(text, encoding="utf-8")
        return root

    def test_a_clean_root_passes(self, tmp_path, capsys):
        root = self.write(tmp_path, GOOD)
        assert main(["check-stack", str(root)]) == 0
        assert "7 file(s) checked" in capsys.readouterr().out

    def test_a_forbidden_block_fails_with_an_annotation(self, tmp_path, capsys):
        root = self.write(tmp_path, {**GOOD, "evil.tf": 'data "external" "e" {\n  program = ["id"]\n}\n'})
        assert main(["check-stack", str(root)]) == 1
        captured = capsys.readouterr()
        assert "::error file=" in captured.out and "evil.tf" in captured.out and "[block]" in captured.out
        assert "1 problem(s)" in captured.err

    def test_a_symlink_is_refused(self, tmp_path, capsys):
        root = self.write(tmp_path, GOOD)
        outside = tmp_path / "outside.tf"
        outside.write_text("", encoding="utf-8")
        os.symlink(outside, root / "link.tf")
        assert main(["check-stack", str(root)]) == 1
        assert "[symlink]" in capsys.readouterr().out

    def test_a_symlinked_directory_is_refused(self, tmp_path, capsys):
        root = self.write(tmp_path, GOOD)
        os.symlink(tmp_path, root / "dir")
        assert main(["check-stack", str(root)]) == 1

    def test_non_utf8_and_oversized_files_are_refused(self, tmp_path, capsys):
        root = self.write(tmp_path, GOOD)
        (root / "bad.tf").write_bytes(b"\xff\xfe\x00")
        (root / "big.tf").write_text("#" * ((1 << 20) + 1), encoding="utf-8")
        assert main(["check-stack", str(root)]) == 1
        out = capsys.readouterr().out
        assert "not UTF-8" in out and "larger than" in out

    def test_a_missing_directory_is_an_error(self, tmp_path, capsys):
        assert main(["check-stack", str(tmp_path / "nope")]) == 2

    def test_the_repository_stacks_pass(self):
        """Every stack root committed under environments/ is accepted."""
        repo = Path(__file__).resolve().parents[3]
        roots = [p for p in (repo / "environments").glob("*/*/*") if p.is_dir()]
        assert roots, "no stack roots found"
        for root in roots:
            assert main(["check-stack", str(root)]) == 0, root

    def test_everything_the_generator_emits_passes(self):
        """The golden outputs (local backend, s3 backend, destroy) are what the guard must accept."""
        repo = Path(__file__).resolve().parents[3]
        golden = [p for p in (repo / "backend/internal/generator/testdata/golden").iterdir() if (p / "main.tf").exists()]
        assert len(golden) >= 3
        for root in golden:
            assert main(["check-stack", str(root)]) == 0, root
