import pytest

from tools.manifest_validator import diffing, hcl


class TestHcl:
    def test_strip_comments_keeps_strings(self):
        text = 'a = "x # not a comment" # comment\n// line\n/* block\n */b = 1'
        assert hcl.strip_comments(text) == 'a = "x # not a comment" \n\nb = 1'

    def test_find_blocks_ignores_comments_and_string_braces(self):
        text = '''
variable "a" {
  description = "has { brace"
  type = string
}
# variable "ghost" { type = string }
output "o" { value = 1 }
'''
        assert set(hcl.find_blocks(text, "variable")) == {"a"}
        assert set(hcl.find_blocks(text, "output")) == {"o"}

    @pytest.mark.parametrize(
        "body,family",
        [
            ("type = string", "string"),
            ("type = number", "number"),
            ("type = bool", "bool"),
            ("type = map(string)", "map"),
            ("type = object({ a = string })", "map"),
            ("type = list(string)", "list"),
            ("type = set(string)", "list"),
            ("type = any", "any"),
            ("default = 1", None),
        ],
    )
    def test_type_family(self, body, family):
        assert hcl.type_family(body) == family

    def test_locals_and_string_attribute(self):
        text = 'locals {\n  module_version = "1.2.3"\n  x = 1\n}\nlocals { y = 2 }'
        assert hcl.string_attribute(hcl.find_locals(text), "module_version") == "1.2.3"
        assert hcl.string_attribute(hcl.find_locals(text), "missing") is None

    def test_unbalanced_braces_raise(self):
        with pytest.raises(ValueError):
            hcl.block_body("{ {", 0)


class TestDiffing:
    def test_key_diff(self):
        assert diffing.key_diff(["a", "b"], ["b", "c"]) == (["a"], ["c"])

    @pytest.mark.parametrize(
        "prop,expected",
        [
            ({"type": "string"}, {"string"}),
            ({"type": ["string", "null"]}, {"string"}),
            ({}, set()),
        ],
    )
    def test_json_types(self, prop, expected):
        assert diffing.json_types(prop) == frozenset(expected)

    @pytest.mark.parametrize(
        "family,types,ok",
        [
            ("string", {"string"}, True),
            ("number", {"integer"}, True),
            ("number", {"number"}, True),
            ("bool", {"boolean"}, True),
            ("map", {"object"}, True),
            ("list", {"array"}, True),
            ("any", {"string"}, True),
            (None, {"string"}, True),
            ("string", {"integer"}, False),
            ("bool", {"string", "boolean"}, False),
            ("string", set(), False),
            ("unknown", {"string"}, False),
        ],
    )
    def test_types_compatible(self, family, types, ok):
        assert diffing.types_compatible(family, frozenset(types)) is ok

    def test_json_path(self):
        assert diffing.json_path(["metadata", "owners", 0]) == "metadata.owners[0]"
        assert diffing.json_path([]) == "<root>"

    def test_schema_errors(self):
        assert diffing.schema_errors({"type": "integer", "minimum": 1}, 5) == []
        assert diffing.schema_errors({"type": "integer", "minimum": 1}, 0) == ["0 is less than the minimum of 1"]

    def test_format_names(self):
        assert diffing.format_names(["a", "b"]) == "'a', 'b'"
