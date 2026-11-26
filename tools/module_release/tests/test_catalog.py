"""Catalog payload and client tests against an in-process fake HTTP server."""

from __future__ import annotations

import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Iterator

import pytest
import yaml

from tools.module_release import CatalogClient, CatalogError, build_index_payload, index_url
from tools.module_release.cli import main

from .conftest import REPO_ROOT, manifest

SHA = "9c" * 20
TOKEN = "test-internal-token-0123456789abcdef"

# Fields of catalog.IndexCommand / catalog.Output in backend/internal/catalog. The
# backend decodes strictly, so the payload must not contain anything else.
INDEX_COMMAND_FIELDS = {
    "module", "version", "git_tag", "commit_sha", "display_name", "description", "category", "owners",
    "inputs_schema", "outputs", "cost_hints", "tf_version_range", "aws_provider_range", "changelog_md",
    "released_by",
}
OUTPUT_FIELDS = {"name", "description", "sensitive"}


class TestPayload:
    def test_maps_manifest_to_index_command(self):
        payload = build_index_payload(manifest("1.3.0", "sqs"), SHA, "release-bot", "### Added\n- x")
        assert payload["module"] == "sqs" and payload["version"] == "1.3.0"
        assert payload["git_tag"] == "modules/sqs/v1.3.0" and payload["commit_sha"] == SHA
        assert payload["display_name"] == "Demo" and payload["category"] == "storage"
        assert payload["owners"] == ["@acme/platform"]
        assert payload["inputs_schema"]["required"] == ["name"]
        assert payload["outputs"] == [{"name": "id", "description": "Identifier", "sensitive": False}]
        assert payload["tf_version_range"] == ">= 1.10, < 2.0"
        assert payload["aws_provider_range"] == ">= 5.60, < 6.0"
        assert payload["cost_hints"] == {"baseline_monthly_usd": 1.0, "drivers": ["requests"]}
        assert payload["changelog_md"] == "### Added\n- x" and payload["released_by"] == "release-bot"

    def test_optional_fields_omitted(self):
        m = manifest()
        del m["cost"]
        payload = build_index_payload(m, SHA, "bot")
        assert "cost_hints" not in payload and "changelog_md" not in payload

    def test_rejects_invalid_version(self):
        m = manifest()
        m["metadata"]["version"] = "1.0"
        with pytest.raises(ValueError):
            build_index_payload(m, SHA, "bot")

    @pytest.mark.parametrize("module", ["s3", "sqs", "lambda", "vpc", "monitoring", "dynamodb"])
    def test_repository_manifests_only_use_backend_fields(self, module):
        data = yaml.safe_load((REPO_ROOT / "modules" / module / "module.yaml").read_text())
        payload = build_index_payload(data, SHA, "bot", "notes")
        assert set(payload) <= INDEX_COMMAND_FIELDS
        assert all(set(o) == OUTPUT_FIELDS for o in payload["outputs"])
        assert payload["git_tag"] == f"modules/{module}/v{payload['version']}"
        assert all(payload[k] for k in ("display_name", "description", "tf_version_range", "aws_provider_range"))
        json.dumps(payload)  # serialisable


class TestIndexUrl:
    @pytest.mark.parametrize(
        "base,url",
        [
            ("https://iacp.example.com", "https://iacp.example.com/v1/internal/catalog/index"),
            ("https://iacp.example.com/api/", "https://iacp.example.com/api/v1/internal/catalog/index"),
            ("http://localhost:8080", "http://localhost:8080/v1/internal/catalog/index"),
            ("http://127.0.0.1:8080/", "http://127.0.0.1:8080/v1/internal/catalog/index"),
        ],
    )
    def test_valid(self, base, url):
        assert index_url(base) == url

    @pytest.mark.parametrize("base", ["http://iacp.example.com", "ftp://x", "iacp.example.com", ""])
    def test_invalid(self, base):
        with pytest.raises(ValueError):
            index_url(base)


class FakeCatalog:
    """Records requests and replies with a scripted sequence of (status, body)."""

    def __init__(self) -> None:
        self.requests: list[dict] = []
        self.replies: list[tuple[int, dict]] = []

    def handler(self):
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802 (http.server API)
                length = int(self.headers.get("Content-Length", 0))
                fake.requests.append({
                    "path": self.path,
                    "headers": dict(self.headers),
                    "body": json.loads(self.rfile.read(length) or b"{}"),
                })
                status, body = fake.replies.pop(0) if fake.replies else (201, {"module": "demo"})
                location = body.pop("_location", None)
                raw = json.dumps(body).encode()
                self.send_response(status)
                if location:
                    self.send_header("Location", location)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):  # noqa: N802 (http.server API)
                fake.requests.append({"path": self.path, "headers": dict(self.headers), "body": None})
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *args):
                pass

        return Handler


@pytest.fixture
def catalog() -> Iterator[tuple[FakeCatalog, str]]:
    fake = FakeCatalog()
    server = ThreadingHTTPServer(("127.0.0.1", 0), fake.handler())
    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True)
    thread.start()
    try:
        yield fake, f"http://127.0.0.1:{server.server_address[1]}"
    finally:
        server.shutdown()
        server.server_close()


def client(base: str, **kwargs) -> CatalogClient:
    return CatalogClient(base, TOKEN, sleep=lambda _: None, timeout=5, **kwargs)


class TestCatalogClient:
    def test_created(self, catalog):
        fake, base = catalog
        result = client(base).index({"module": "demo"})
        assert result.created and result.status == 201 and result.body == {"module": "demo"}
        request = fake.requests[0]
        assert request["path"] == "/v1/internal/catalog/index"
        assert request["headers"]["Authorization"] == f"Bearer {TOKEN}"
        assert request["headers"]["Content-Type"] == "application/json"
        assert request["body"] == {"module": "demo"}

    def test_replay_is_success(self, catalog):
        fake, base = catalog
        fake.replies = [(200, {"module": "demo"})]
        result = client(base).index({"module": "demo"})
        assert not result.created and result.status == 200

    def test_validation_error_is_not_retried(self, catalog):
        fake, base = catalog
        fake.replies = [(422, {"title": "Request failed validation", "detail": "module release is invalid",
                               "errors": [{"pointer": "/category", "code": "enum", "message": "must be one of"}]})]
        with pytest.raises(CatalogError) as exc:
            client(base).index({"module": "demo"})
        assert exc.value.status == 422 and len(fake.requests) == 1
        assert "/category must be one of" in str(exc.value)

    @pytest.mark.parametrize("status", [401, 409])
    def test_client_errors(self, catalog, status):
        fake, base = catalog
        fake.replies = [(status, {"title": "nope"})]
        with pytest.raises(CatalogError, match=f"HTTP {status}: nope"):
            client(base).index({})

    def test_server_errors_are_retried(self, catalog):
        fake, base = catalog
        fake.replies = [(503, {}), (500, {"title": "Internal server error"}), (201, {})]
        sleeps: list[float] = []
        result = CatalogClient(base, TOKEN, sleep=sleeps.append, backoff=1.5).index({})
        assert result.created and len(fake.requests) == 3 and sleeps == [1.5, 3.0]

    def test_gives_up_after_attempts(self, catalog):
        fake, base = catalog
        fake.replies = [(502, {})] * 3
        with pytest.raises(CatalogError, match="HTTP 502"):
            client(base, attempts=2).index({})
        assert len(fake.requests) == 2

    @pytest.mark.parametrize("status", [301, 302, 303, 307, 308])
    def test_redirects_are_refused(self, catalog, status):
        fake, base = catalog
        fake.replies = [(status, {"_location": f"{base}/elsewhere"})]
        with pytest.raises(CatalogError, match=f"HTTP {status} redirect to .*/elsewhere.* refused") as exc:
            client(base).index({})
        assert exc.value.status == status
        assert [r["path"] for r in fake.requests] == ["/v1/internal/catalog/index"]  # not followed

    def test_unexpected_success_status_is_an_error(self, catalog):
        fake, base = catalog
        fake.replies = [(202, {})]
        with pytest.raises(CatalogError, match="unexpected HTTP 202"):
            client(base).index({})

    def test_connection_refused(self):
        with pytest.raises(CatalogError, match="catalog index failed"):
            client("http://127.0.0.1:9", attempts=1).index({})

    def test_requires_token(self):
        with pytest.raises(ValueError, match="IACP_INTERNAL_TOKEN"):
            CatalogClient("https://iacp.example.com", "")


class TestIndexCommand:
    def test_posts_repository_module(self, catalog, tmp_path, capsys):
        fake, base = catalog
        env = {"IACP_API_URL": base, "IACP_INTERNAL_TOKEN": TOKEN}
        assert main(["--repo", str(REPO_ROOT), "index", "s3", "--commit-sha", SHA, "--released-by", "ci"], env=env) == 0
        assert "modules/s3/v" in capsys.readouterr().out
        body = fake.requests[0]["body"]
        assert body["module"] == "s3" and body["commit_sha"] == SHA and body["released_by"] == "ci"
        assert body["changelog_md"].startswith("### Added")

    def test_reports_rejection(self, catalog, capsys):
        fake, base = catalog
        fake.replies = [(409, {"title": "Request conflicts with current state", "detail": "already indexed"})]
        env = {"IACP_API_URL": base, "IACP_INTERNAL_TOKEN": TOKEN}
        assert main(["--repo", str(REPO_ROOT), "index", "sqs", "--commit-sha", SHA], env=env) == 1
        assert "HTTP 409" in capsys.readouterr().err


@pytest.mark.skipif(os.environ.get("IACP_INTEGRATION") != "1" or not os.environ.get("IACP_API_URL"),
                    reason="needs IACP_INTEGRATION=1 and a running API (make up && make api)")
@pytest.mark.parametrize("module", ["s3", "sqs", "lambda", "vpc", "monitoring", "dynamodb"])
def test_index_against_running_api(module: str):
    """The backend accepts each repository manifest; a second call is an idempotent replay.

    A random module name keeps the real s3/sqs/lambda catalog rows untouched.
    """
    data = yaml.safe_load((REPO_ROOT / "modules" / module / "module.yaml").read_text())
    data["metadata"]["name"] = f"it-{module}-{os.urandom(4).hex()}"
    payload = build_index_payload(data, os.urandom(20).hex(), "integration-test")
    api = CatalogClient(os.environ["IACP_API_URL"], os.environ["IACP_INTERNAL_TOKEN"])
    first = api.index(payload)
    assert first.created and first.body["git_tag"] == payload["git_tag"]
    assert not api.index(payload).created
