"""The static stack-root check that runs before init and plan (stack_guard.py)."""

import os
from pathlib import Path

import pytest

from tools.stack_pipeline.cli import main
from tools.stack_pipeline.stack_guard import (
    ALLOWED_FILES,
    HclScanError,
    check_entries,
    check_lock,
    check_lock_pins,
    check_stack,
    check_tf,
    lock_allowlist,
    lock_entries,
    parse_blocks,
    provider_sources,
    strip_comments,
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
        assert check_lock("l", 'provider "registry.terraform.io/hashicorp/random" {\n  version = "3.7.2"\n}\n') == []

    @pytest.mark.parametrize("address", ["registry.terraform.io/hashicorp/external", "registry.terraform.io/hashicorp/http",
                                         "evil.example/x/aws", "registry.terraform.io/evil/aws"])
    def test_other_providers_are_refused(self, address):
        assert [v.rule for v in check_lock("l", f'provider "{address}" {{\n  version = "1"\n}}\n')] == ["provider"]

    def test_other_blocks_and_unscannable_text_are_refused(self):
        assert check_lock("l", 'resource "a" "b" {\n}\n') != []
        assert [v.rule for v in check_lock("l", 'provider "x" {')] == ["unreadable"]

    def test_the_lock_file_is_scanned_through_check_stack(self):
        files = {**GOOD, ".terraform.lock.hcl": 'provider "registry.terraform.io/hashicorp/external" {\n  version = "2.3.0"\n}\n'}
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
        root = self.write(tmp_path, {**GOOD, "outputs.tf": 'data "external" "e" {\n  program = ["id"]\n}\n'})
        assert main(["check-stack", str(root)]) == 1
        captured = capsys.readouterr()
        assert "::error file=" in captured.out and "outputs.tf" in captured.out and "[block]" in captured.out
        assert "1 problem(s)" in captured.err

    def test_a_symlink_is_refused(self, tmp_path, capsys):
        root = self.write(tmp_path, GOOD)
        outside = tmp_path / "outside.tf"
        outside.write_text("", encoding="utf-8")
        os.symlink(outside, root / "link.tf")
        assert main(["check-stack", str(root)]) == 1
        assert "[entry]" in capsys.readouterr().out

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


class TestEntries:
    def test_the_generated_files_pass(self):
        assert check_entries({name: "file" for name in ALLOWED_FILES}) == []
        assert check_entries({}) == []

    @pytest.mark.parametrize("name,kind", [
        (".terraform", "dir"), (".terraform.d", "dir"), ("providers", "dir"), ("modules", "dir"),
        ("evil.tf", "file"), ("README.md", "file"), (".gitkeep", "file"), (".terraformrc", "file"),
        ("terraform.tfvars", "file"), ("main.tf", "symlink"), ("fifo", "other"), ("override.tf", "file"),
    ])
    def test_everything_else_is_refused(self, name, kind):
        (v,) = check_entries({"main.tf": "file", name: kind} if name != "main.tf" else {name: kind})
        assert (v.path, v.rule) == (name, "entry")

    def test_the_message_explains_the_provider_binary_route(self):
        (v,) = check_entries({".terraform": "dir"})
        assert "provider binaries" in v.message and "main.tf" in v.message


class TestCommandEntries:
    def write(self, tmp_path, files):
        root = tmp_path / "environments" / "staging" / "payments" / "orders"
        root.mkdir(parents=True)
        for name, text in files.items():
            (root / name).write_text(text, encoding="utf-8")
        return root

    def test_a_committed_provider_binary_is_refused(self, tmp_path, capsys):
        """The reproduced bypass: a provider package under .terraform, reused by init without a download."""
        root = self.write(tmp_path, GOOD)
        binary = root / ".terraform" / "providers" / "registry.terraform.io" / "hashicorp" / "aws" / "5.100.0" / "linux_amd64"
        binary.mkdir(parents=True)
        (binary / "terraform-provider-aws_v5.100.0_x5").write_text("#!/bin/sh\nid\n", encoding="utf-8")
        assert main(["check-stack", str(root)]) == 1
        out = capsys.readouterr().out
        assert "[entry]" in out and ".terraform" in out

    def test_a_plugin_cache_in_the_stack_root_is_refused(self, tmp_path, capsys):
        root = self.write(tmp_path, GOOD)
        (root / ".terraform.d" / "plugin-cache").mkdir(parents=True)
        assert main(["check-stack", str(root)]) == 1
        assert ".terraform.d" in capsys.readouterr().out

    def test_an_extra_tf_file_is_refused(self, tmp_path, capsys):
        root = self.write(tmp_path, {**GOOD, "extra.tf": "# nothing\n"})
        assert main(["check-stack", str(root)]) == 1
        assert "extra.tf" in capsys.readouterr().out


AWS_HASHES = ["h1:aaaa+/=", "zh:1111", "zh:2222"]


def lock_text(address="registry.terraform.io/hashicorp/aws", version="5.100.0", hashes=AWS_HASHES):
    listed = "".join(f'    "{h}",\n' for h in hashes)
    return (f'provider "{address}" {{\n  version     = "{version}"\n  constraints = "~> 5.80"\n'
            f'  hashes = [\n{listed}  ]\n}}\n')


class TestLockPins:
    def pins(self):
        return lock_allowlist([lock_text(), lock_text("registry.terraform.io/hashicorp/random", "3.7.2", ["h1:rrrr", "zh:3333"])])

    def test_entries_and_the_allow_list(self):
        assert lock_entries(lock_text()) == [("hashicorp/aws", "5.100.0", frozenset(AWS_HASHES))]
        pins = self.pins()
        assert set(pins) == {("hashicorp/aws", "5.100.0"), ("hashicorp/random", "3.7.2")}
        merged = lock_allowlist([lock_text(hashes=["h1:aaaa", "zh:1111"]), lock_text(hashes=["h1:bbbb"]), "provider {"])
        assert merged[("hashicorp/aws", "5.100.0")] == {"h1:aaaa", "zh:1111", "h1:bbbb"}  # unreadable text adds nothing

    def test_a_known_lock_passes_and_a_subset_of_hashes_too(self):
        assert check_lock_pins("l", lock_text(), self.pins()) == []
        assert check_lock_pins("l", lock_text(hashes=["zh:1111"]), self.pins()) == []

    def test_a_made_up_package_hash_is_refused(self):
        """The attacker's own provider binary has its own h1 hash, which no base lock file knows."""
        (v,) = check_lock_pins("l", lock_text(hashes=["h1:forged+/=", "zh:1111"]), self.pins())
        assert v.rule == "lock" and "h1:forged" in v.message

    def test_an_unknown_version_or_provider_is_refused(self):
        assert [v.rule for v in check_lock_pins("l", lock_text(version="5.101.0"), self.pins())] == ["lock"]
        assert [v.rule for v in check_lock_pins("l", lock_text("registry.terraform.io/hashicorp/aws", "", AWS_HASHES), self.pins())] == ["lock"]

    def test_a_lock_with_no_hashes_is_refused(self):
        (v,) = check_lock_pins("l", lock_text(hashes=[]), self.pins())
        assert "no hashes" in v.message

    def test_check_stack_applies_pins_only_when_given(self):
        files = {**GOOD, ".terraform.lock.hcl": lock_text(hashes=["h1:forged"])}
        assert check_stack(files) == []
        assert [v.rule for v in check_stack(files, pins=self.pins())] == ["lock"]
        assert check_stack({**GOOD, ".terraform.lock.hcl": lock_text()}, pins=self.pins()) == []


class TestCommandLockFrom:
    def base(self, tmp_path, text=None):
        d = tmp_path / "base" / "environments" / "development" / "platform" / "smoke"
        d.mkdir(parents=True)
        (d / ".terraform.lock.hcl").write_text(text if text is not None else lock_text(), encoding="utf-8")
        return tmp_path / "base"

    def stack(self, tmp_path, lock):
        root = tmp_path / "environments" / "staging" / "payments" / "orders"
        root.mkdir(parents=True)
        for name, text in {**GOOD, ".terraform.lock.hcl": lock}.items():
            (root / name).write_text(text, encoding="utf-8")
        return root

    def test_a_lock_the_base_knows_passes(self, tmp_path):
        assert main(["check-stack", str(self.stack(tmp_path, lock_text())), "--lock-from", str(self.base(tmp_path))]) == 0

    def test_a_forged_hash_fails(self, tmp_path, capsys):
        root = self.stack(tmp_path, lock_text(hashes=["h1:forged+/="]))
        assert main(["check-stack", str(root), "--lock-from", str(self.base(tmp_path))]) == 1
        assert "[lock]" in capsys.readouterr().out

    def test_a_base_without_any_lock_is_an_error_not_a_pass(self, tmp_path, capsys):
        base = tmp_path / "base" / "environments"
        base.mkdir(parents=True)
        root = self.stack(tmp_path, lock_text())
        assert main(["check-stack", str(root), "--lock-from", str(tmp_path / "base")]) == 2
        assert "no provider pins" in capsys.readouterr().err

    def test_the_repository_stacks_match_their_own_locks(self):
        repo = Path(__file__).resolve().parents[3]
        for root in (p for p in (repo / "environments").glob("*/*/*") if p.is_dir()):
            assert main(["check-stack", str(root), "--lock-from", str(repo)]) == 0, root


class TestEscapedHashes:
    """The reviewed PoC: Terraform decodes HCL escapes in the lock file, a regex over the raw text does not."""

    def pins(self):
        return lock_allowlist([lock_text()])

    def poisoned(self, forged='h1:forged\\u003d'):
        # one hash the base knows, plus the attacker's package hash with its trailing "=" written as an escape
        return ('provider "registry.terraform.io/hashicorp/aws" {\n  version = "5.100.0"\n  constraints = "~> 5.80"\n'
                f'  hashes = [\n    "zh:1111",\n    "{forged}",\n  ]\n}}\n')

    def test_the_escaped_hash_is_refused_by_the_pin_check_alone(self):
        (v,) = check_lock_pins("l", self.poisoned(), self.pins())
        assert v.rule == "lock" and "does not know" in v.message

    def test_the_escaped_hash_is_refused_by_check_lock(self):
        found = check_lock("l", self.poisoned())
        assert [v.rule for v in found] == ["lock"] and "backslash" in found[0].message and found[0].line == 6

    def test_check_stack_refuses_it_twice(self):
        found = check_stack({**GOOD, ".terraform.lock.hcl": self.poisoned()}, pins=self.pins())
        assert [v.rule for v in found] == ["lock", "lock"]

    @pytest.mark.parametrize("forged", ["h1:forged\\x3d", "h1:\\u0066orged=", "h1:${var.h}", "h1:%{ if true }x%{ endif }",
                                        "h1:forged\\n", "h1:\\\\"])
    def test_every_escape_and_template_form_is_refused(self, forged):
        assert check_stack({**GOOD, ".terraform.lock.hcl": self.poisoned(forged)}, pins=self.pins()) != []
        assert check_lock("l", self.poisoned(forged)) != []

    def test_a_plain_non_hash_string_in_the_list_is_refused_too(self):
        found = check_lock_pins("l", self.poisoned("not a hash at all"), self.pins())
        assert [v.rule for v in found] == ["lock"]

    def test_a_clean_lock_is_unaffected(self):
        assert check_lock("l", lock_text()) == []
        assert check_stack({**GOOD, ".terraform.lock.hcl": lock_text()}, pins=self.pins()) == []

    def test_the_command_refuses_the_poisoned_lock(self, tmp_path, capsys):
        base = tmp_path / "base" / "environments" / "development" / "platform" / "smoke"
        base.mkdir(parents=True)
        (base / ".terraform.lock.hcl").write_text(lock_text(), encoding="utf-8")
        root = tmp_path / "environments" / "staging" / "payments" / "orders"
        root.mkdir(parents=True)
        for name, text in {**GOOD, ".terraform.lock.hcl": self.poisoned()}.items():
            (root / name).write_text(text, encoding="utf-8")
        assert main(["check-stack", str(root), "--lock-from", str(tmp_path / "base")]) == 1
        assert "[lock]" in capsys.readouterr().out


def raw_lock(hashes_block, version='version = "5.100.0"', before=""):
    return (f'provider "registry.terraform.io/hashicorp/aws" {{\n{before}  {version}\n  constraints = "~> 5.80"\n'
            f'{hashes_block}}}\n')


class TestLockComments:
    """Comments must not steer what the pin check reads (reviewed PoCs: Terraform ignores them, a regex did not)."""

    def pins(self):
        return lock_allowlist([lock_text()])

    def test_a_comment_hiding_a_closing_bracket_does_not_end_the_list(self):
        text = raw_lock('  hashes = [\n    "zh:1111", # ]\n    "h1:evil",\n  ]\n')
        assert lock_entries(text)[0][2] == {"zh:1111", "h1:evil"}
        (v,) = check_lock_pins("l", text, self.pins())
        assert v.rule == "lock" and "h1:evil" in v.message

    def test_a_commented_out_hashes_above_the_real_one_is_ignored(self):
        text = raw_lock('  # hashes = ["zh:1111"]\n  hashes = [\n    "h1:evil",\n  ]\n')
        assert lock_entries(text)[0][2] == {"h1:evil"}
        assert [v.rule for v in check_lock_pins("l", text, self.pins())] == ["lock"]

    @pytest.mark.parametrize("comment", ['// hashes = ["zh:1111"]\n', '/* hashes = ["zh:1111"] */\n',
                                         '/* multi\n line\n hashes = ["zh:1111"]\n */\n', '# version = "5.100.0"\n'])
    def test_every_comment_form_is_ignored(self, comment):
        text = raw_lock('  hashes = [\n    "h1:evil",\n  ]\n', before=comment)
        assert lock_entries(text)[0][2] == {"h1:evil"}
        assert check_lock_pins("l", text, self.pins()) != []

    def test_a_commented_version_does_not_stand_in_for_the_real_one(self):
        text = raw_lock('  hashes = [\n    "zh:1111",\n  ]\n', version='# version = "5.100.0"\n  version = "9.9.9"')
        assert lock_entries(text)[0][1] == "9.9.9"
        assert [v.rule for v in check_lock_pins("l", text, self.pins())] == ["lock"]

    def test_comment_markers_inside_strings_are_kept(self):
        text = raw_lock('  hashes = [\n    "zh:11//11",\n    "h1:a#b",\n    "h1:c/*d*/",\n  ]\n')
        assert lock_entries(text)[0][2] == {"zh:11//11", "h1:a#b", "h1:c/*d*/"}

    def test_a_bracket_inside_a_string_does_not_end_the_list(self):
        text = raw_lock('  hashes = [\n    "zh:1111]",\n    "h1:evil",\n  ]\n')
        assert lock_entries(text)[0][2] == {"zh:1111]", "h1:evil"}

    def test_a_heredoc_in_the_lock_is_refused(self):
        text = raw_lock('  hashes = [\n    <<EOT\nzh:1111\nEOT\n    ,\n  ]\n')
        found = check_lock("l", text)
        assert any("heredoc" in v.message for v in found)
        assert check_stack({**GOOD, ".terraform.lock.hcl": text}, pins=self.pins()) != []

    @pytest.mark.parametrize("hashes", ['  hashes = ["zh:1"]\n  hashes = ["h1:evil"]\n', '  hashes = "zh:1"\n',
                                        '  hashes = [["zh:1"]]\n'])
    def test_an_unreadable_hash_list_is_refused(self, hashes):
        text = raw_lock(hashes)
        assert any(v.rule == "unreadable" for v in check_lock("l", text))
        assert check_stack({**GOOD, ".terraform.lock.hcl": text}, pins=self.pins()) != []

    def test_two_version_attributes_are_refused(self):
        text = raw_lock('  hashes = ["zh:1111"]\n', before='  version = "1.0.0"\n')
        assert any(v.rule == "unreadable" for v in check_lock("l", text))

    def test_a_missing_hashes_attribute_means_no_hashes_not_a_pass(self):
        text = raw_lock("")
        assert lock_entries(text)[0][2] == frozenset()
        assert [v.rule for v in check_lock_pins("l", text, self.pins())] == ["lock"]

    def test_strip_comments_keeps_line_numbers(self):
        text = 'a\n/* x\ny */ b # c\n"#" // d\n'
        out = strip_comments(text)
        assert out.count("\n") == text.count("\n") and '"#"' in out and "c" not in out.replace("\n", "")
        with pytest.raises(HclScanError):
            strip_comments("/* never closed")

    def test_a_real_lock_file_is_unchanged(self):
        assert lock_entries(lock_text()) == [("hashicorp/aws", "5.100.0", frozenset(AWS_HASHES))]
        assert check_lock("l", lock_text()) == []
