"""Tests for notify_master_failures.py"""

import sys
from unittest.mock import MagicMock, patch

import pytest

from scripts.evergreen.notify_flaky_tests import FlakyTask
from scripts.evergreen.notify_master_failures import (
    TaskInfo,
    VersionInfo,
    format_slack_message,
    get_failed_and_running_tasks,
    print_stdout_report,
    send_slack_notification,
)


def make_flaky_map_entry(percent: float, total_runs: int) -> FlakyTask:
    """Create a FlakyTask for testing."""
    return FlakyTask(task_name="", variant="", flakiness_percent=percent, total_runs=total_runs)


def make_mock_task(display_name: str, build_variant: str, status: str = "failed") -> TaskInfo:
    """Create a mock TaskInfo for testing."""
    mock_task = MagicMock()
    mock_task.task_id = f"task_{display_name}"
    mock_task.display_name = display_name
    mock_task.status = status
    # status_details is an object with .desc attribute, not a dict
    mock_task.status_details = MagicMock(desc="")
    return TaskInfo(task=mock_task, build_variant=build_variant)


def make_mock_version(
    version_id: str = "test_version",
    revision: str = "abc12345def",
    author: str = "Test Author",
    author_id: str = "test.author",
    message: str = "Test commit",
    status: str = "succeeded",
    order: int = 100,
) -> VersionInfo:
    """Create a mock VersionInfo for testing."""
    mock_version = MagicMock()
    mock_version.version_id = version_id
    mock_version.revision = revision
    mock_version.author = author
    mock_version.message = message
    mock_version.status = status
    mock_version.order = order
    mock_version.json = {"author_id": author_id}
    mock_version.build_variants_status = []
    return VersionInfo(version=mock_version)


FEW_TASKS = [
    make_mock_task("e2e_task_1", "variant_a"),
    make_mock_task("e2e_task_2", "variant_a"),
    make_mock_task("e2e_task_3", "variant_b"),
]
MANY_TASKS = [make_mock_task(f"e2e_task_{i}", f"variant_{i % 3}") for i in range(15)]


def make_message(failed_tasks=None, **kwargs):
    version_info = make_mock_version(**kwargs)
    return format_slack_message(version_info, failed_tasks)


class TestFormatSlackMessage:
    def test_success_message(self):
        message = make_message()
        assert "passed" in message["text"]
        assert "Master Build Passed" in message["blocks"][0]["text"]["text"]

    def test_failure_message(self):
        message = make_message(failed_tasks=FEW_TASKS)
        assert "3 tasks failed" in message["text"]
        assert "Master Build Failures" in message["blocks"][0]["text"]["text"]

    def test_includes_run_number(self):
        message = make_message(failed_tasks=FEW_TASKS, order=6411)
        assert "Run #6411" in message["text"]
        run_block = message["blocks"][1]["fields"][0]["text"]
        assert "#6411" in run_block

    def test_groups_failures_by_variant(self):
        message = make_message(failed_tasks=FEW_TASKS)
        text = message["blocks"][5]["text"]["text"]
        assert text.index("variant_a") < text.index("variant_b")

    def test_uses_first_line_of_commit(self):
        message = make_message(message="First line\n\nSecond line")
        text = message["blocks"][3]["text"]["text"]
        assert "First line" in text
        assert "Second line" not in text

    def test_truncates_many_failures(self):
        message = make_message(failed_tasks=MANY_TASKS)
        text = message["blocks"][5]["text"]["text"]
        assert "15 tasks failed" in text
        assert "task_0" not in text

    def test_shows_details_for_few_failures(self):
        message = make_message(failed_tasks=FEW_TASKS)
        text = message["blocks"][5]["text"]["text"]
        assert "e2e_task_1" in text

    def test_flakiness_includes_total_runs_in_slack(self):
        flaky_map = {
            ("e2e_task_1", "variant_a"): make_flaky_map_entry(50, 2),
            ("e2e_task_2", "variant_a"): make_flaky_map_entry(25, 100),
        }
        version_info = make_mock_version(status="failed")
        message = format_slack_message(version_info, FEW_TASKS[:2], flaky_map)
        # Find the failed tasks block (after divider)
        text = ""
        for block in message["blocks"]:
            if block["type"] == "section" and "text" in block and "Failed Tasks" in block["text"].get("text", ""):
                text = block["text"]["text"]
                break
        assert "over 2 runs" in text
        assert "over 100 runs" in text

    def test_flakiness_summary_collapses_when_min_equals_max(self):
        """Summary should show 'over N runs' (no range) when all flaky tasks have same total_runs."""
        flaky_map = {
            ("e2e_task_1", "variant_a"): make_flaky_map_entry(50, 47),
            ("e2e_task_2", "variant_a"): make_flaky_map_entry(25, 47),
        }
        version_info = make_mock_version(status="failed")
        message = format_slack_message(version_info, FEW_TASKS[:2], flaky_map)
        # Find the flakiness summary block
        summary_text = ""
        for block in message["blocks"]:
            if block["type"] == "section" and "text" in block and "flaky behavior" in block["text"].get("text", ""):
                summary_text = block["text"]["text"]
                break
        assert "over 47 runs" in summary_text
        assert "–" not in summary_text

    def test_flakiness_summary_shows_range_when_min_ne_max(self):
        """Summary should show 'over lo–hi runs' range when flaky tasks have different total_runs."""
        flaky_map = {
            ("e2e_task_1", "variant_a"): make_flaky_map_entry(50, 18),
            ("e2e_task_2", "variant_a"): make_flaky_map_entry(25, 47),
        }
        version_info = make_mock_version(status="failed")
        message = format_slack_message(version_info, FEW_TASKS[:2], flaky_map)
        summary_text = ""
        for block in message["blocks"]:
            if block["type"] == "section" and "text" in block and "flaky behavior" in block["text"].get("text", ""):
                summary_text = block["text"]["text"]
                break
        assert "over 18–47 runs" in summary_text


class TestPrintStdoutReport:
    def _report(self, failed_tasks=None, running_tasks=None, **kwargs):
        version_info = make_mock_version(**kwargs)
        print_stdout_report(version_info, failed_tasks or [], running_tasks or [])

    def test_running_status(self, capsys):
        self._report(status="started")
        assert "BUILD STARTED:" in capsys.readouterr().out

    def test_failed_status(self, capsys):
        self._report(status="failed")
        assert "BUILD FAILED:" in capsys.readouterr().out

    def test_includes_run_number(self, capsys):
        self._report(status="failed", order=6411)
        assert "Run #6411" in capsys.readouterr().out

    def test_shows_failed_tasks(self, capsys):
        self._report(failed_tasks=FEW_TASKS, status="failed")
        assert "Failed Tasks (3):" in capsys.readouterr().out

    def test_flakiness_includes_total_runs(self, capsys):
        flaky_map = {
            ("e2e_task_1", "variant_a"): make_flaky_map_entry(50, 2),
        }
        version_info = make_mock_version(status="failed")
        print_stdout_report(version_info, FEW_TASKS[:1], [], flaky_map)
        out = capsys.readouterr().out
        assert "flaky: 50% over 2 runs" in out


class TestGetFailedAndRunningTasks:
    @patch("scripts.evergreen.notify_master_failures.get_build_tasks")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_skips_successful_builds(self, mock_version, mock_tasks):
        # Create mock build variant statuses
        mock_bv1 = MagicMock()
        mock_bv1.build_id = "b1"
        mock_bv1.build_variant = "ok"
        mock_bv1.json = {"status": "success"}

        mock_bv2 = MagicMock()
        mock_bv2.build_id = "b2"
        mock_bv2.build_variant = "fail"
        mock_bv2.json = {"status": "failed"}

        mock_ver = make_mock_version()
        mock_ver.version.build_variants_status = [mock_bv1, mock_bv2]
        mock_version.return_value = mock_ver

        mock_tasks.return_value = [make_mock_task("e2e_t1", "fail", "failed")]
        mock_api = MagicMock()

        get_failed_and_running_tasks(mock_api, "v1")

        # Should only call get_build_tasks for the failed build variant
        mock_tasks.assert_called_once_with(mock_api, mock_bv2)

    @patch("scripts.evergreen.notify_master_failures.get_build_tasks")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_excludes_aborted_from_failures(self, mock_version, mock_tasks):
        # Create mock build variant status
        mock_bv = MagicMock()
        mock_bv.build_id = "b1"
        mock_bv.build_variant = "v1"
        mock_bv.json = {"status": None}

        mock_ver = make_mock_version()
        mock_ver.version.build_variants_status = [mock_bv]
        mock_version.return_value = mock_ver

        # Create tasks: one real failure, one aborted
        real_fail_task = MagicMock()
        real_fail_task.task_id = "task_real_fail"
        real_fail_task.display_name = "e2e_real_fail"
        real_fail_task.status = "failed"
        # status_details is an object with .desc attribute, not a dict
        real_fail_task.status_details = MagicMock(desc="")

        aborted_task = MagicMock()
        aborted_task.task_id = "task_aborted"
        aborted_task.display_name = "e2e_aborted"
        aborted_task.status = "failed"
        aborted_task.status_details = MagicMock(desc="aborted")

        mock_tasks.return_value = [
            TaskInfo(task=real_fail_task, build_variant="v1"),
            TaskInfo(task=aborted_task, build_variant="v1"),
        ]
        mock_api = MagicMock()

        failed, _ = get_failed_and_running_tasks(mock_api, "v1")

        assert len(failed) == 1
        assert failed[0].display_name == "e2e_real_fail"

class TestSendSlackNotification:
    @patch("scripts.evergreen.notify_master_failures.requests.post")
    def test_success(self, mock_post):
        mock_post.return_value.raise_for_status = MagicMock()
        assert send_slack_notification("http://test", {"text": "hi"}) is True

    @patch("scripts.evergreen.notify_master_failures.requests.post")
    def test_failure(self, mock_post):
        from requests.exceptions import RequestException

        mock_post.side_effect = RequestException("fail")
        assert send_slack_notification("http://test", {"text": "hi"}) is False


class TestMainOutputBehavior:
    @pytest.fixture
    def mocks(self, monkeypatch):
        with (
            patch("scripts.evergreen.notify_master_failures.get_evergreen_api") as api,
            patch("scripts.evergreen.notify_master_failures.wait_for_version_completion") as wait,
            patch("scripts.evergreen.notify_master_failures.get_failed_and_running_tasks") as tasks,
            patch("scripts.evergreen.notify_master_failures.send_slack_notification") as slack,
            patch("scripts.evergreen.notify_master_failures.print_stdout_report") as stdout,
        ):
            api.return_value = MagicMock()
            slack.return_value = True
            monkeypatch.setenv("version_id", "test")
            monkeypatch.delenv("SLACK_WEBHOOK_URL", raising=False)
            monkeypatch.delenv("NOTIFY_ON_SUCCESS", raising=False)
            yield {"wait": wait, "tasks": tasks, "slack": slack, "stdout": stdout}

    def _run(self, mocks, status, failed=None, running=None, args=None):
        mocks["wait"].return_value = make_mock_version(
            revision="abc", status=status, author="X", author_id="x", message="m"
        )
        # Convert dict tasks to TaskInfo if provided as dicts
        failed_tasks = [make_mock_task(t["display_name"], t["build_variant"]) for t in (failed or [])]
        running_tasks = [make_mock_task(t["display_name"], t["build_variant"], "started") for t in (running or [])]
        mocks["tasks"].return_value = (failed_tasks, running_tasks)
        sys.argv = ["test.py"] + (args or [])
        from scripts.evergreen.notify_master_failures import main

        main()

    def test_stdout_without_webhook(self, mocks):
        self._run(mocks, "failed", failed=[{"build_variant": "v", "display_name": "e2e_t"}])
        assert mocks["stdout"].called
        assert not mocks["slack"].called

    def test_slack_with_failures(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(mocks, "failed", failed=[{"build_variant": "v", "display_name": "e2e_t"}])
        assert mocks["slack"].called

    def test_no_slack_for_running_only(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(mocks, "started", running=[{"build_variant": "v", "display_name": "e2e_t"}])
        assert not mocks["slack"].called

    def test_slack_on_success_by_default(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(mocks, "success")
        assert mocks["slack"].called

    def test_no_slack_on_success_when_disabled(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        monkeypatch.setenv("NOTIFY_ON_SUCCESS", "false")
        self._run(mocks, "success")
        assert not mocks["slack"].called

    def test_dry_run_no_slack(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(mocks, "failed", failed=[{"build_variant": "v", "display_name": "e2e_t"}], args=["--dry-run"])
        assert not mocks["slack"].called
