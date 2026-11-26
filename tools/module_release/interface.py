"""Pure extraction of a module's public interface from Terraform source text.

Parsing uses python-hcl2. The functions here take file contents (not paths),
so callers decide whether the text comes from the working tree or a git tag.
"""

from __future__ import annotations

import re
from typing import Any, Iterable, Mapping

import hcl2
from hcl2 import SerializationOptions

from .models import Interface, Output, Variable

_OPTIONS = SerializationOptions(with_comments=False, explicit_blocks=False, strip_string_quotes=True)
_WHITESPACE = re.compile(r"\s+")


class InterfaceError(ValueError):
    """A Terraform file could not be parsed."""


def normalise_expression(raw: Any) -> str:
    """Render an HCL expression in a canonical form: no ``${}`` wrapper, no whitespace."""
    text = str(raw).strip()
    if text.startswith("${") and text.endswith("}"):
        text = text[2:-1]
    return _WHITESPACE.sub("", text)


def parse_variable(name: str, body: Mapping[str, Any]) -> Variable:
    """Build a Variable from one parsed ``variable`` block body."""
    validations = body.get("validation") or []
    if isinstance(validations, Mapping):
        validations = [validations]
    conditions = tuple(
        sorted(normalise_expression(v.get("condition", "")) for v in validations if isinstance(v, Mapping))
    )
    return Variable(
        name=name,
        type=normalise_expression(body["type"]) if "type" in body else "any",
        required="default" not in body,
        default=body.get("default"),
        nullable=body.get("nullable", True) is not False,
        validations=conditions,
    )


def _labelled_blocks(document: Mapping[str, Any], kind: str) -> Iterable[tuple[str, Mapping[str, Any]]]:
    for block in document.get(kind) or []:
        for label, body in block.items():
            yield label, body if isinstance(body, Mapping) else {}


def parse_document(text: str, filename: str = "<string>") -> Mapping[str, Any]:
    """Parse one HCL file into python-hcl2's dictionary form."""
    try:
        return hcl2.loads(text, serialization_options=_OPTIONS)
    except Exception as exc:  # python-hcl2 raises lark errors of several types
        raise InterfaceError(f"{filename}: cannot parse HCL: {exc}") from exc


def parse_interface(files: Mapping[str, str]) -> Interface:
    """Return the interface declared across ``files`` (filename -> contents)."""
    variables: dict[str, Variable] = {}
    outputs: dict[str, Output] = {}
    for filename in sorted(files):
        if not filename.endswith(".tf"):
            continue
        document = parse_document(files[filename], filename)
        variables.update((n, parse_variable(n, b)) for n, b in _labelled_blocks(document, "variable"))
        outputs.update((n, Output(n)) for n, _ in _labelled_blocks(document, "output"))
    return Interface(variables=variables, outputs=outputs)
