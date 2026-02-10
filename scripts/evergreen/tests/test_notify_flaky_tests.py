"""Tests for notify_flaky_tests.py"""

from unittest.mock import MagicMock, patch

import pytest

from scripts.evergreen.tests.notify_flaky_tests import (
    FlakyTest,
    abbreviate_variant,
    format_slack_message,
    format_test_name,
    get_honeycomb_headers,
)


def make_flaky_test(
    name: str = "test_file.py::TestClass::test_method",
    task: str = "e2e_task",
    failure_count: int = 5,
    total_runs: int = 20,
    failure_rate: float = 0.25,
    avg_duration_ms: float = 3600000.0,  # 60 minutes
) -> FlakyTest:
    """Create a FlakyTest for testing."""
    return FlakyTest(
        name=name,
        task=task,
        failure_count=failure_count,
        total_runs=total_runs,
        failure_rate=failure_rate,
        avg_duration_ms=avg_duration_ms,
    )


class TestFormatTestName:
    def test_short_name_unchanged(self):
        name = "test_short.py::test_func"
        assert format_test_name(name) == name

    def test_long_name_extracts_function(self):
        name = "very/long/path/to/test_file.py::TestVeryLongClassName::test_very_long_method_name"
        result = format_test_name(name, max_length=40)
        assert result == "test_very_long_method_name"


class TestAbbreviateVariant:
    def test_removes_e2e_prefix(self):
        assert abbreviate_variant("e2e_om70_kind_ubi") == "om70"

    def test_removes_kind_ubi_suffix(self):
        assert abbreviate_variant("e2e_static_om70_kind_ubi") == "static-om70"

    def test_removes_kind_suffix(self):
        assert abbreviate_variant("e2e_multi_cluster_kind") == "multi-cluster"

    def test_removes_cloudqa_suffix(self):
        assert abbreviate_variant("e2e_mdb_kind_ubi_cloudqa") == "mdb"

    def test_converts_underscores_to_hyphens(self):
        assert abbreviate_variant("e2e_multi_cluster_om_appdb") == "multi-cluster-om-appdb"


class TestFormatSlackMessage:
    def test_empty_list_shows_no_flaky_tests(self):
        message = format_slack_message([])
        assert "No flaky tests found" in message["blocks"][1]["text"]["text"]

    def test_header_present(self):
        message = format_slack_message([make_flaky_test()])
        assert message["blocks"][0]["type"] == "header"
        assert "Weekly Flaky Test Report" in message["blocks"][0]["text"]["text"]

    def test_shows_top_5_header(self):
        tests = [make_flaky_test(name=f"test_{i}") for i in range(5)]
        message = format_slack_message(tests)
        assert "Top 5" in message["blocks"][1]["text"]["text"]

    def test_shows_task_name(self):
        message = format_slack_message([make_flaky_test(task="my_e2e_task")])
        # Find the section block with the task name
        found = False
        for block in message["blocks"]:
            if block.get("type") == "section":
                text = block.get("text", {}).get("text", "")
                if "my_e2e_task" in text:
                    found = True
                    break
        assert found

    def test_shows_failure_rate(self):
        message = format_slack_message([make_flaky_test(failure_count=5, total_runs=20)])
        # Should show "5/20 runs (25%)"
        found = False
        for block in message["blocks"]:
            if block.get("type") == "section":
                text = block.get("text", {}).get("text", "")
                if "5/20" in text and "25%" in text:
                    found = True
                    break
        assert found

    def test_shows_duration(self):
        # 60 minutes = 3600000 ms
        message = format_slack_message([make_flaky_test(avg_duration_ms=3600000)])
        found = False
        for block in message["blocks"]:
            if block.get("type") == "section":
                text = block.get("text", {}).get("text", "")
                if "60 min" in text:
                    found = True
                    break
        assert found

    def test_shows_only_top_5(self):
        tests = [make_flaky_test(name=f"test_{i}") for i in range(20)]
        message = format_slack_message(tests)
        # Count test sections (exclude header, summary, context, dividers, honeycomb link)
        test_sections = [
            b for b in message["blocks"] if b.get("type") == "section" and "test_" in b.get("text", {}).get("text", "")
        ]
        assert len(test_sections) == 5

    def test_includes_honeycomb_link(self):
        message = format_slack_message([make_flaky_test()])
        found = False
        for block in message["blocks"]:
            if block.get("type") == "context":
                for elem in block.get("elements", []):
                    if "View in Honeycomb" in elem.get("text", ""):
                        found = True
                        break
        assert found


class TestGetHoneycombHeaders:
    def test_missing_api_key_raises(self, monkeypatch):
        monkeypatch.delenv("HONEYCOMB_API_KEY", raising=False)
        with pytest.raises(RuntimeError, match="HONEYCOMB_API_KEY"):
            get_honeycomb_headers()

    def test_returns_headers_with_key(self, monkeypatch):
        monkeypatch.setenv("HONEYCOMB_API_KEY", "test-key")
        headers = get_honeycomb_headers()
        assert headers["X-Honeycomb-Team"] == "test-key"
        assert headers["Content-Type"] == "application/json"


class TestFlakyTestDataclass:
    def test_creation(self):
        test = make_flaky_test()
        assert test.name == "test_file.py::TestClass::test_method"
        assert test.task == "e2e_task"
        assert test.failure_count == 5
        assert test.total_runs == 20
        assert test.failure_rate == 0.25
        assert test.avg_duration_ms == 3600000.0


class TestHoneycombQueries:
    @patch("scripts.evergreen.tests.notify_flaky_tests.create_and_run_query")
    def test_get_task_avg_duration_minutes_handles_error(self, mock_query, monkeypatch):
        from requests.exceptions import RequestException

        from scripts.evergreen.tests.notify_flaky_tests import get_task_avg_duration_minutes

        mock_query.side_effect = RequestException("API error")
        monkeypatch.setenv("HONEYCOMB_API_KEY", "test")

        result = get_task_avg_duration_minutes({"X-Honeycomb-Team": "test"}, ["task1"])
        assert result == {}

    @patch("scripts.evergreen.tests.notify_flaky_tests.create_and_run_query")
    def test_get_task_avg_duration_minutes_calculates_correctly(self, mock_query):
        from scripts.evergreen.tests.notify_flaky_tests import get_task_avg_duration_minutes

        # Mock response: task with 120000ms total over 2 executions = 60000ms avg = 1 minute
        mock_query.return_value = {
            "data": {
                "results": [
                    {
                        "data": {
                            "evergreen.task.name": "test_task",
                            "SUM(duration_ms)": 120000,
                            "COUNT_DISTINCT(evergreen.task.id)": 2,
                        }
                    }
                ]
            }
        }

        result = get_task_avg_duration_minutes({"X-Honeycomb-Team": "test"}, ["test_task"])
        assert result == {"test_task": 1.0}  # 1 minute

    @patch("scripts.evergreen.tests.notify_flaky_tests.create_and_run_query")
    def test_get_task_avg_duration_minutes_empty_for_no_tasks(self, mock_query):
        from scripts.evergreen.tests.notify_flaky_tests import get_task_avg_duration_minutes

        result = get_task_avg_duration_minutes({"X-Honeycomb-Team": "test"}, [])
        assert result == {}
        mock_query.assert_not_called()


class TestSlackNotification:
    @patch("scripts.evergreen.tests.notify_flaky_tests.requests.post")
    def test_send_slack_notification_success(self, mock_post, monkeypatch):
        from scripts.evergreen.tests.notify_flaky_tests import send_slack_notification

        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://test-webhook")
        mock_post.return_value.raise_for_status = MagicMock()

        send_slack_notification({"text": "test"})
        mock_post.assert_called_once()

    def test_send_slack_notification_missing_url(self, monkeypatch):
        from scripts.evergreen.tests.notify_flaky_tests import send_slack_notification

        monkeypatch.delenv("SLACK_WEBHOOK_URL", raising=False)
        with pytest.raises(RuntimeError, match="SLACK_WEBHOOK_URL"):
            send_slack_notification({"text": "test"})
