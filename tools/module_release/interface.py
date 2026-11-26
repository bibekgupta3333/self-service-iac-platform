"""Pure extraction of a module's public interface from Terraform source text.

Parsing uses python-hcl2. The functions here take file contents (not paths),
so callers decide whether the text comes from the working tree or a git tag.
"""

from __future__ import annotations

from typing import Any, Iterable, Mapping

import hcl2
from hcl2 import SerializationOptions

from .models import Interface, Output, Variable

_OPTIONS = SerializationOptions(with_comments=False, explicit_blocks=False, strip_string_quotes=True)


class InterfaceError(ValueError):
    """A Terraform file could not be parsed."""


def strip_whitespace_outside_strings(text: str) -> str:
    """Remove whitespace that is not inside a ``"..."`` string literal (escapes respected)."""
    out: list[str] = []
    in_string = escaped = False
    for ch in text:
        if in_string:
            out.append(ch)
            if escaped:
                escaped = False
            elif ch == "\\":
                escaped = True
            elif ch == '"':
                in_string = False
        elif ch == '"':
            in_string = True
            out.append(ch)
        elif not ch.isspace():
            out.append(ch)
    return "".join(out)


def normalise_expression(raw: Any) -> str:
    """Render an HCL expression in a canonical form: no ``${}`` wrapper and no
    whitespace outside string literals (string contents are kept verbatim)."""
    text = str(raw).strip()
    if text.startswith("${") and text.endswith("}"):
        text = text[2:-1]
    return strip_whitespace_outside_strings(text)


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


def parse_output(name: str, body: Mapping[str, Any]) -> Output:
    """Build an Output from one parsed ``output`` block body."""
    return Output(name=name, sensitive=body.get("sensitive") is True)


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


def _required_providers(settings: Mapping[str, Any]) -> dict[str, str | None]:
    blocks = settings.get("required_providers") or []
    if isinstance(blocks, Mapping):
        blocks = [blocks]
    providers: dict[str, str | None] = {}
    for block in blocks:
        for local_name, spec in (block.items() if isinstance(block, Mapping) else []):
            # Terraform's implied source for a provider without one (including the
            # legacy shorthand aws = "~> 5.0") is hashicorp/<local name>.
            if isinstance(spec, Mapping):
                key = str(spec.get("source") or f"hashicorp/{local_name}")
                version = spec.get("version")
            else:
                key, version = f"hashicorp/{local_name}", spec
            providers[key] = None if version is None else str(version)
    return providers


def parse_interface(files: Mapping[str, str]) -> Interface:
    """Return the interface declared across ``files`` (filename -> contents)."""
    variables: dict[str, Variable] = {}
    outputs: dict[str, Output] = {}
    terraform_version: str | None = None
    providers: dict[str, str | None] = {}
    for filename in sorted(files):
        if not filename.endswith(".tf"):
            continue
        document = parse_document(files[filename], filename)
        variables.update((n, parse_variable(n, b)) for n, b in _labelled_blocks(document, "variable"))
        outputs.update((n, parse_output(n, b)) for n, b in _labelled_blocks(document, "output"))
        for settings in document.get("terraform") or []:
            if not isinstance(settings, Mapping):
                continue
            if settings.get("required_version") is not None:
                terraform_version = str(settings["required_version"])
            providers.update(_required_providers(settings))
    return Interface(variables=variables, outputs=outputs, terraform_version=terraform_version, providers=providers)
