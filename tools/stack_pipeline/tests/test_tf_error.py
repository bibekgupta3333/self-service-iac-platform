"""tf_error: the shared Terraform error summary. The cases come from the vectors the Go
tests read too (backend/internal/tferror/testdata/vectors.json)."""

import json
from pathlib import Path

import pytest

from tools.stack_pipeline import tf_error

VECTORS = json.loads(
    (Path(__file__).resolve().parents[3] / "backend" / "internal" / "tferror" / "testdata" / "vectors.json").read_text(
        encoding="utf-8"))


@pytest.mark.parametrize("case", VECTORS["redact"], ids=lambda c: c["in"][:30])
def test_redact(case) -> None:
    assert tf_error.redact(case["in"]) == case["out"]


@pytest.mark.parametrize("case", VECTORS["summary"], ids=lambda c: c["name"])
def test_summary_and_signals(case) -> None:
    got = tf_error.summary(case["in"], case["max_lines"], case["limit"], case["fallback"])
    assert got == case["summary"]
    sig = tf_error.signals(case["in"])
    assert sig == case["signals"]
    joined = got + "\n" + "\n".join(sig)
    for leak in case["never"]:
        assert leak not in joined, leak
    if got != case["fallback"]:  # a summary of a summary is the same summary
        assert tf_error.summary(got, case["max_lines"], case["limit"], case["fallback"]) == got


@pytest.mark.parametrize("case", VECTORS["filter_signals"])
def test_filter_signals(case) -> None:
    assert tf_error.filter_signals(case["in"]) == case["out"]


def test_vocabulary_matches_the_backend() -> None:
    assert list(tf_error.VOCABULARY) == VECTORS["vocabulary"]


def test_cap_and_signal_limit() -> None:
    long = tf_error.summary("Error: " + "x" * 900, 3, 500, "f")
    assert len(long) == 500 and long.endswith("...")
    assert len(tf_error.signals("Error: x (y): " + " ".join(tf_error.VOCABULARY))) == tf_error.MAX_SIGNALS
