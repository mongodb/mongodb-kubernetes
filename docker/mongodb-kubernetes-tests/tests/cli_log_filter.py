"""Pytest plugin: drop DEBUG records from chatty third-party loggers off the
live CLI stream so per-test console output stays focused on test-owned logs.

Without this filter every kubernetes-client REST call dumps a full HTTP
response body (often 1–5 KB of JSON) into the live CLI stream, swamping the
test_logger.get_test_logger() output that actually describes what the test
is asserting. pymongo and urllib3 add similar wire-level chatter on the
later DB-touching tests.

Records are still emitted by the libraries — they remain visible at DEBUG
in the pytest log_file (see pytest.ini → /tmp/results/pytest-debug.log)
for post-hoc forensic analysis. The filter is installed only on the live
CLI handler, so the file log is unaffected.

Test code that uses test_logger.get_test_logger(__name__) keeps its DEBUG
output on the CLI — the filter only drops DEBUG from the listed
third-party namespaces.
"""

import logging

import pytest

_NOISY_NAMESPACES = (
    "kubernetes",
    "urllib3",
    "pymongo",
    "asyncio",
    "boto",
    "botocore",
    "s3transfer",
)


class DropChattyDebug(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        if record.levelno >= logging.INFO:
            return True
        return not any(
            record.name == ns or record.name.startswith(ns + ".") for ns in _NOISY_NAMESPACES
        )


@pytest.hookimpl(trylast=True)
def pytest_configure(config: pytest.Config) -> None:
    plugin = config.pluginmanager.get_plugin("logging-plugin")
    handler = getattr(plugin, "log_cli_handler", None) if plugin else None
    if handler is None:
        return
    handler.addFilter(DropChattyDebug())
