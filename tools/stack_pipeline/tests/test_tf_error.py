"""tf_error: the shared Terraform error summary (mirrors backend/internal/tferror tests)."""

from tools.stack_pipeline import tf_error

BOXED = (
    "aws_db_instance.main: Creating...\n"
    "╷\n"
    "│ Error: creating RDS DB Instance (orders-db): operation error RDS: CreateDBInstance, https response error "
    "StatusCode: 400, api error InvalidParameterValue: The parameter MasterUserPassword hunter2-very-secret is not "
    "valid (AKIAABCDEFGHIJKLMNOP)\n"
    "│ \n"
    "│   with module.db.aws_db_instance.main,\n"
    "│   12:   master_password = \"s3cr3t-in-detail\" # ThrottlingException\n"
    "╵\n"
    "╷\n"
    "│ Error: creating SQS Queue (orders-dlq): operation error SQS: CreateQueue, https response error StatusCode: 503, "
    "api error ThrottlingException: Rate exceeded; Authorization: Bearer abc.def.ghi\n"
    "╵\n"
)
LEAKS = ("hunter2", "AKIAABCDEFGHIJKLMNOP", "s3cr3t-in-detail", "abc.def.ghi", "master_password", "MasterUserPassword", "│")


def test_summary_keeps_only_the_identifier() -> None:
    assert tf_error.summary(BOXED, 3, 500, "f") == "Error: creating RDS DB Instance (orders-db) | Error: creating SQS Queue (orders-dlq)"
    assert tf_error.summary(BOXED, 1, 500, "f") == "Error: creating RDS DB Instance (orders-db)"


def test_excerpt_carries_signals_but_no_secrets() -> None:
    out = tf_error.excerpt(BOXED, 3, 500, "f")
    for leak in LEAKS:
        assert leak not in out, leak
    for signal in ("api error InvalidParameterValue", "api error ThrottlingException", "StatusCode: 503", "Rate exceeded"):
        assert signal in out, signal
    assert len(out) <= 500


def test_signals_read_only_error_lines() -> None:
    assert tf_error.signals("│ Error: creating X (a): boom\n│   12:   note = \"ThrottlingException\"\n") == []
    assert tf_error.signals("│ Error: Error acquiring the state lock\n") == ["Error acquiring the state lock"]


def test_redact() -> None:
    cases = {
        "password=hunter2 x": "password= <redacted> x",
        "Authorization: Bearer abc.def": "Authorization: <redacted>",
        "key AKIAABCDEFGHIJKLMNOP": "key <redacted>",
        "bearer xyz": "<redacted>",
        "tab\there\x07": "tab here ",
    }
    for raw, want in cases.items():
        assert tf_error.redact(raw) == want, raw


def test_fallback_and_cap() -> None:
    assert tf_error.excerpt("Refreshing state...\n  master_password = \"x\"\n", 3, 500, "fallback") == "fallback"
    long = tf_error.summary("Error: " + "x" * 900, 3, 500, "f")
    assert len(long) == 500 and long.endswith("...")
