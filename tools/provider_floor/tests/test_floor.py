from pathlib import Path

import pytest

from tools.provider_floor.__main__ import main
from tools.provider_floor.floor import OVERRIDE_FILE, FloorError, FloorPin, ModuleStager, floor_pins, render_override

REPO_ROOT = Path(__file__).resolve().parents[3]

VERSIONS = """
terraform {
  required_version = ">= 1.10, < 2.0"
  required_providers {
    aws    = { source = "hashicorp/aws", version = ">= 5.70, < 6.0" }
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}
"""


class TestFloorPins:
    def test_pins_each_floor(self):
        pins = floor_pins({"hashicorp/random": "~> 3.6", "hashicorp/aws": ">= 5.70, < 6.0"})
        assert pins == (FloorPin("aws", "hashicorp/aws", "5.70.0"), FloorPin("random", "hashicorp/random", "3.6.0"))

    @pytest.mark.parametrize("constraint", [None, "< 6.0", ""])
    def test_no_lower_bound_is_skipped(self, constraint):
        assert floor_pins({"hashicorp/aws": constraint}) == ()

    def test_unreadable_constraint(self):
        with pytest.raises(FloorError):
            floor_pins({"hashicorp/aws": "latest"})

    def test_render(self):
        text = render_override(floor_pins({"hashicorp/aws": ">= 5.70, < 6.0"}))
        assert 'version = "= 5.70.0"' in text and 'source  = "hashicorp/aws"' in text

    def test_render_needs_a_pin(self):
        with pytest.raises(FloorError):
            render_override(())


class TestModuleStager:
    def make(self, tmp_path):
        mod = tmp_path / "mod"
        (mod / "tests").mkdir(parents=True)
        (mod / ".terraform").mkdir()
        (mod / "examples").mkdir()
        (mod / "versions.tf").write_text(VERSIONS)
        (mod / "tests" / "a.tftest.hcl").write_text("run \"x\" {}\n")
        (mod / ".terraform" / "big").write_text("x")
        return mod

    def test_stages_a_copy_with_the_override(self, tmp_path):
        mod = self.make(tmp_path)
        pins = ModuleStager().stage(mod, tmp_path / "out")
        assert [p.version for p in pins] == ["5.70.0", "3.6.0"]
        out = tmp_path / "out"
        assert (out / "tests" / "a.tftest.hcl").is_file() and (out / "versions.tf").is_file()
        assert not (out / ".terraform").exists() and not (out / "examples").exists()
        assert 'version = "= 3.6.0"' in (out / OVERRIDE_FILE).read_text()
        assert not (mod / OVERRIDE_FILE).exists()  # the source is untouched

    def test_refuses_a_non_empty_destination(self, tmp_path):
        mod = self.make(tmp_path)
        (tmp_path / "out").mkdir()
        (tmp_path / "out" / "x").write_text("x")
        with pytest.raises(FloorError):
            ModuleStager().stage(mod, tmp_path / "out")

    def test_missing_module(self, tmp_path):
        with pytest.raises(FloorError):
            ModuleStager().stage(tmp_path / "nope", tmp_path / "out")

    def test_cli(self, tmp_path, capsys):
        mod = self.make(tmp_path)
        assert main(["stage", str(mod), str(tmp_path / "out")]) == 0
        assert capsys.readouterr().out.split() == ["hashicorp/aws=5.70.0", "hashicorp/random=3.6.0"]
        assert main(["stage", str(tmp_path / "nope"), str(tmp_path / "o2")]) == 2

    @pytest.mark.parametrize("name", ["ecs-service", "postgres", "redis"])
    def test_real_offline_modules_have_a_floor(self, name, tmp_path):
        pins = ModuleStager().stage(REPO_ROOT / "modules" / name, tmp_path / name)
        assert FloorPin("aws", "hashicorp/aws", "5.70.0") in pins
