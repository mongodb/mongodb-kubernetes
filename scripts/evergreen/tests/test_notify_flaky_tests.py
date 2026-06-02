"""Tests for notify_flaky_tests.py"""

from unittest.mock import MagicMock, patch

import pytest

from scripts.evergreen.notify_flaky_tests import FlakyTask, format_slack_message, get_honeycomb_headers


def make_flaky_task(
    task_name: str = "e2e_task",
    variant: str = "e2e_variant",
    flakiness_percent: float = 25.0,
) -> FlakyTask:
    """Create a FlakyTask for testing."""
    return FlakyTask(
        task_name=task_name,
        variant=variant,
        flakiness_percent=flakiness_percent,
    )


class TestFormatSlackMessage:
    def test_empty_list_shows_no_flaky_tasks(self):
        message = format_slack_message([])
        assert "No flaky tasks found" in message["blocks"][1]["text"]["text"]

    def test_header_present(self):
        message = format_slack_message([make_flaky_task()])
        assert message["blocks"][0]["type"] == "header"
        assert "Weekly Flaky Task Report" in message["blocks"][0]["text"]["text"]

    def test_shows_top_5_header(self):
        tasks = [make_flaky_task(task_name=f"task_{i}") for i in range(5)]
        message = format_slack_message(tasks)
        assert "Top 5" in message["blocks"][1]["text"]["text"]

    def test_shows_task_name(self):
        message = format_slack_message([make_flaky_task(task_name="my_e2e_task")])
        found = False
        for block in message["blocks"]:
            if block.get("type") == "section":
                text = block.get("text", {}).get("text", "")
                if "my_e2e_task" in text:
                    found = True
                    break
        assert found

    def test_shows_variant(self):
        message = format_slack_message([make_flaky_task(variant="my_variant")])
        found = False
        for block in message["blocks"]:
            if block.get("type") == "section":
                text = block.get("text", {}).get("text", "")
                if "my_variant" in text:
                    found = True
                    break
        assert found

    def test_shows_flakiness_percent(self):
        message = format_slack_message([make_flaky_task(flakiness_percent=33.3)])
        found = False
        for block in message["blocks"]:
            if block.get("type") == "section":
                text = block.get("text", {}).get("text", "")
                if "33.3%" in text:
                    found = True
                    break
        assert found

    def test_shows_only_top_5(self):
        tasks = [make_flaky_task(task_name=f"task_{i}") for i in range(20)]
        message = format_slack_message(tasks)
        # Count task sections (exclude header, summary, context, dividers, honeycomb link)
        task_sections = [
            b for b in message["blocks"] if b.get("type") == "section" and "task_" in b.get("text", {}).get("text", "")
        ]
        assert len(task_sections) == 5

    def test_includes_honeycomb_link(self):
        message = format_slack_message([make_flaky_task()])
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


class TestFlakyTaskDataclass:
    def test_creation(self):
        task = make_flaky_task()
        assert task.task_name == "e2e_task"
        assert task.variant == "e2e_variant"
        assert task.flakiness_percent == 25.0


class TestSlackNotification:
    @patch("scripts.evergreen.notify_flaky_tests.requests.post")
    def test_send_slack_notification_success(self, mock_post, monkeypatch):
        from scripts.evergreen.notify_flaky_tests import send_slack_notification

        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://test-webhook")
        mock_post.return_value.raise_for_status = MagicMock()

        send_slack_notification({"text": "test"})
        mock_post.assert_called_once()

    def test_send_slack_notification_missing_url(self, monkeypatch):
        from scripts.evergreen.notify_flaky_tests import send_slack_notification

        monkeypatch.delenv("SLACK_WEBHOOK_URL", raising=False)
        with pytest.raises(RuntimeError, match="SLACK_WEBHOOK_URL"):
            send_slack_notification({"text": "test"})
