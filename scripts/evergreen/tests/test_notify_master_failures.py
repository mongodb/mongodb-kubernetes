"""Tests for notify_master_failures.py"""

import sys
from dataclasses import replace
from unittest.mock import MagicMock, patch

import pytest

from scripts.evergreen.notify_flaky_tests import FlakyTask
from scripts.evergreen.notify_master_failures import (
    NOTIFICATION_TASKS,
    TaskInfo,
    VersionInfo,
    format_slack_message,
    get_build_tasks,
    get_previous_version_failures,
    get_task_summary,
    print_stdout_report,
    send_slack_notification,
    wait_for_version_completion,
)


def make_flaky_map_entry(percent: float, total_runs: int) -> FlakyTask:
    """Create a FlakyTask for testing."""
    return FlakyTask(task_name="", variant="", flakiness_percent=percent, total_runs=total_runs)


def make_mock_task(display_name: str, build_variant: str, status: str = "failed", execution: int = 0) -> TaskInfo:
    """Create a mock TaskInfo for testing."""
    mock_task = MagicMock()
    mock_task.task_id = f"task_{display_name}"
    mock_task.display_name = display_name
    mock_task.display_only = False
    mock_task.execution = execution
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
    branch: str = "master",
) -> VersionInfo:
    """Create a mock VersionInfo for testing."""
    mock_version = MagicMock()
    mock_version.version_id = version_id
    mock_version.revision = revision
    mock_version.author = author
    mock_version.message = message
    mock_version.status = status
    mock_version.order = order
    mock_version.branch = branch
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

    def test_backport_branch_failure_message(self):
        message = make_message(failed_tasks=FEW_TASKS, branch="release-v1")
        assert "release-v1 build failed" in message["text"]
        assert "release-v1 Build Failures" in message["blocks"][0]["text"]["text"]

    def test_backport_branch_success_message(self):
        message = make_message(branch="release-v2")
        assert "release-v2 build passed" in message["text"]
        assert "release-v2 Build Passed" in message["blocks"][0]["text"]["text"]

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

    def test_prev_failed_in_slack(self):
        """Summary line + per-task annotation when tasks also failed on previous master."""
        prev_failed = {("e2e_task_1", "variant_a"), ("e2e_task_2", "variant_a")}
        version_info = make_mock_version(status="failed")
        message = format_slack_message(version_info, FEW_TASKS, prev_failed=prev_failed)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "2 of 3" in text
        assert "previous master" in text
        assert "also on previous master" in text

    def test_success_includes_retry_count(self):
        retried = [make_mock_task(f"e2e_retry_{i}", "variant_a", "success", execution=1) for i in range(3)]
        version_info = make_mock_version(status="succeeded")
        message = format_slack_message(version_info, retried_tasks=retried, total_tasks=1400)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "3 of 1400 task runs restarted before this pass" in text
        assert "3 of 1400 task runs restarted before this pass" in message["text"]

    def test_success_reports_no_restarts(self):
        version_info = make_mock_version(status="succeeded")
        message = format_slack_message(version_info, retried_tasks=[], total_tasks=1400)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "no tasks restarted" in text

    def test_failure_omits_retry_count(self):
        retried = [make_mock_task("e2e_retry_1", "variant_a", "success", execution=1)]
        version_info = make_mock_version(status="failed")
        message = format_slack_message(version_info, FEW_TASKS, retried_tasks=retried, total_tasks=1400)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "restarted" not in text

    def test_parent_lists_a_capped_sample_of_tasks(self):
        retried = [make_mock_task(f"e2e_retry_{i}", "variant_a", "success", execution=1) for i in range(6)]
        version_info = make_mock_version(status="succeeded")
        message = format_slack_message(
            version_info,
            retried_tasks=retried,
            total_tasks=1400,
        )
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "6 of 1400 task runs restarted before this pass" in text
        assert "Tasks: e2e_retry_0, e2e_retry_1, e2e_retry_2, e2e_retry_3, e2e_retry_4 and 1 more" in text
        assert "runs)" not in text

    def test_success_lists_restarted_task_names(self):
        retried = [
            make_mock_task("e2e_a", "variant_a", "success", execution=1),
            make_mock_task("e2e_b", "variant_b", "success", execution=1),
        ]
        version_info = make_mock_version(status="succeeded")
        message = format_slack_message(version_info, retried_tasks=retried, total_tasks=100)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "Tasks: e2e_a, e2e_b" in text

    def test_retry_task_names_are_deduplicated(self):
        retried = [
            make_mock_task("e2e_a", "variant_a", "success", execution=1),
            make_mock_task("e2e_a", "variant_b", "success", execution=1),
        ]
        version_info = make_mock_version(status="succeeded")
        message = format_slack_message(version_info, retried_tasks=retried, total_tasks=100)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert text.count("e2e_a") == 1

    def test_summary_reports_unique_task_names(self):
        retried = [
            make_mock_task("e2e_a", "variant_a", "success", execution=1),
            make_mock_task("e2e_a", "variant_b", "success", execution=1),
            make_mock_task("e2e_b", "variant_a", "success", execution=1),
        ]
        version_info = make_mock_version(status="succeeded")
        message = format_slack_message(version_info, retried_tasks=retried, total_tasks=100)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "3 of 100 task runs restarted before this pass (2 unique task names)" in text

    def test_retry_task_names_are_capped(self):
        retried = [make_mock_task(f"e2e_{i:02d}", "variant_a", "success", execution=1) for i in range(10)]
        version_info = make_mock_version(status="succeeded")
        message = format_slack_message(version_info, retried_tasks=retried, total_tasks=100)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "Tasks: e2e_00, e2e_01, e2e_02, e2e_03, e2e_04 and 5 more" in text

    def test_partial_pass_lists_skipped_variants(self):
        skipped = [make_mock_task("e2e_replica_set", "e2e_smoke_ibm_z", "undispatched")]
        version_info = make_mock_version(status="started")
        message = format_slack_message(version_info, skipped_tasks=skipped)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "Master Build Passed" in message["blocks"][0]["text"]["text"]
        assert "Partial" not in message["blocks"][0]["text"]["text"]
        assert "Skipped variants" in text
        assert "e2e_smoke_ibm_z" in text
        assert "Completed results remain included" in text

    def test_partial_pass_caps_and_sorts_skipped_variants(self):
        variant_names = [
            "e2e_variant_3",
            "e2e_variant_1",
            "e2e_variant_2",
            "e2e_variant_5",
            "e2e_variant_4",
            "e2e_variant_7",
            "e2e_variant_6",
        ]
        skipped = [make_mock_task("e2e_replica_set", name, "undispatched") for name in variant_names]
        version_info = make_mock_version(status="started")
        message = format_slack_message(version_info, skipped_tasks=skipped)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        for i in range(1, 6):
            assert f"e2e_variant_{i}" in text
        positions = [text.index(f"e2e_variant_{i}") for i in range(1, 6)]
        assert positions == sorted(positions)
        assert "e2e_variant_6" not in text
        assert "e2e_variant_7" not in text
        assert "and 2 more" in text
        assert "task(s)" not in text

    def test_failure_includes_skipped_variants(self):
        skipped = [make_mock_task("e2e_replica_set", "e2e_smoke_ibm_z", "undispatched")]
        version_info = make_mock_version(status="failed")
        message = format_slack_message(version_info, FEW_TASKS, skipped_tasks=skipped)
        text = " ".join(
            b["text"]["text"] for b in message["blocks"] if b["type"] == "section" and isinstance(b.get("text"), dict)
        )
        assert "Master Build Failures" in message["blocks"][0]["text"]["text"]
        assert "Skipped variants" in text
        assert "e2e_smoke_ibm_z" in text


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


class TestGetTaskSummary:
    @patch("scripts.evergreen.notify_master_failures.get_build_tasks")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_fetches_all_builds(self, mock_version, mock_tasks):
        """All builds are fetched: the version API returns no build status, and a
        passing version must report the tasks that restarted before the pass."""
        mock_bv1 = MagicMock()
        mock_bv1.build_id = "b1"
        mock_bv1.build_variant = "ok"
        mock_bv1.json = {"build_variant": "ok", "build_id": "b1"}

        mock_bv2 = MagicMock()
        mock_bv2.build_id = "b2"
        mock_bv2.build_variant = "fail"
        mock_bv2.json = {"build_variant": "fail", "build_id": "b2"}

        mock_ver = make_mock_version()
        mock_ver.version.build_variants_status = [mock_bv1, mock_bv2]
        mock_version.return_value = mock_ver

        mock_tasks.return_value = [make_mock_task("e2e_t1", "fail", "failed")]
        mock_api = MagicMock()

        get_task_summary(mock_api, "v1")

        assert mock_tasks.call_count == 2

    @patch("scripts.evergreen.notify_master_failures.get_build_tasks")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_excludes_aborted_from_failures(self, mock_version, mock_tasks):
        # Create mock build variant status
        mock_bv = MagicMock()
        mock_bv.build_id = "b1"
        mock_bv.build_variant = "v1"
        mock_bv.json = {"build_variant": "v1", "build_id": "b1"}

        mock_ver = make_mock_version()
        mock_ver.version.build_variants_status = [mock_bv]
        mock_version.return_value = mock_ver

        # Create tasks: one real failure, one aborted
        real_fail_task = MagicMock()
        real_fail_task.task_id = "task_real_fail"
        real_fail_task.display_name = "e2e_real_fail"
        real_fail_task.display_only = False
        real_fail_task.execution = 0
        real_fail_task.status = "failed"
        # status_details is an object with .desc attribute, not a dict
        real_fail_task.status_details = MagicMock(desc="")

        aborted_task = MagicMock()
        aborted_task.task_id = "task_aborted"
        aborted_task.display_name = "e2e_aborted"
        aborted_task.display_only = False
        aborted_task.execution = 0
        aborted_task.status = "failed"
        aborted_task.status_details = MagicMock(desc="aborted")

        mock_tasks.return_value = [
            TaskInfo(task=real_fail_task, build_variant="v1"),
            TaskInfo(task=aborted_task, build_variant="v1"),
        ]
        mock_api = MagicMock()

        failed, _, _, _ = get_task_summary(mock_api, "v1")

        assert len(failed) == 1
        assert failed[0].display_name == "e2e_real_fail"

    @patch("scripts.evergreen.notify_master_failures.get_build_tasks")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_collects_restarted_tasks(self, mock_version, mock_tasks):
        mock_bv = MagicMock()
        mock_bv.build_id = "b1"
        mock_bv.build_variant = "v1"
        mock_bv.json = {"build_variant": "v1", "build_id": "b1"}

        mock_ver = make_mock_version()
        mock_ver.version.build_variants_status = [mock_bv]
        mock_version.return_value = mock_ver

        first_run = make_mock_task("e2e_first_run", "v1", "success", execution=0)
        restarted_once = make_mock_task("e2e_restarted_once", "v1", "success", execution=1)
        restarted_twice = make_mock_task("e2e_restarted_twice", "v1", "success", execution=2)
        mock_tasks.return_value = [first_run, restarted_once, restarted_twice]

        _, _, retried, total = get_task_summary(MagicMock(), "v1")

        assert total == 3
        assert {t.display_name for t in retried} == {"e2e_restarted_once", "e2e_restarted_twice"}

    @patch("scripts.evergreen.notify_master_failures.get_build_tasks")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_excludes_display_tasks(self, mock_version, mock_tasks):
        mock_bv = MagicMock()
        mock_bv.build_id = "b1"
        mock_bv.build_variant = "v1"
        mock_bv.json = {"build_variant": "v1", "build_id": "b1"}
        mock_ver = make_mock_version()
        mock_ver.version.build_variants_status = [mock_bv]
        mock_version.return_value = mock_ver

        display_task = make_mock_task("e2e_display", "v1", "success", execution=1)
        display_task.task.display_only = True
        mock_tasks.return_value = [display_task]

        _, _, retried, total = get_task_summary(MagicMock(), "v1")

        assert total == 0
        assert retried == []

    @patch("scripts.evergreen.notify_master_failures.get_build_tasks")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_excludes_ignored_tasks(self, mock_version, mock_tasks):
        mock_bv = MagicMock()
        mock_bv.build_id = "b1"
        mock_bv.build_variant = "mckci_release_promote"
        mock_bv.json = {"build_variant": "mckci_release_promote", "build_id": "b1"}
        mock_ver = make_mock_version()
        mock_ver.version.build_variants_status = [mock_bv]
        mock_version.return_value = mock_ver
        mock_tasks.return_value = [
            make_mock_task("mckci_release_promote", "mckci_release_promote", "undispatched"),
            make_mock_task("build_test_image_ibm_z", "init_test_run_ibm_z", "started"),
        ]

        failed, running, retried, total = get_task_summary(MagicMock(), "v1", NOTIFICATION_TASKS)

        assert failed == []
        assert running == []
        assert retried == []
        assert total == 0


class TestCloudQASummary:
    @pytest.mark.parametrize(
        "tags,activated,status,expected_total,expected_retried",
        [
            (["staging", "cloudqa"], False, "undispatched", 0, 0),
            (["staging", "cloudqa"], True, "undispatched", 1, 1),
            (["staging", "cloudqa"], False, "started", 1, 1),
            (["staging", "cloudqa"], False, "dispatched", 1, 1),
            (["staging", "cloudqa"], False, "success", 1, 1),
            (["staging", "cloudqa"], False, "failed", 1, 1),
            (["staging"], False, "undispatched", 1, 1),
            (["cloudqa"], False, "undispatched", 1, 1),
        ],
    )
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_activation_filter_uses_build_tags(
        self, mock_version, tags, activated, status, expected_total, expected_retried
    ):
        task = make_mock_task("e2e_task", "cloudqa_in_name_only", status, execution=1)
        task.task.activated = activated
        variant = MagicMock(build_id="b1", build_variant=task.build_variant)
        version = make_mock_version()
        version.version.build_variants_status = [variant]
        mock_version.return_value = version
        api = MagicMock()
        api.build_by_id.return_value.tags = tags
        api.build_by_id.return_value.get_tasks.return_value = [task.task]

        failed, pending, retried, total = get_task_summary(api, "v1")

        assert total == expected_total
        assert len(retried) == expected_retried
        assert bool(failed) == (status == "failed")
        assert len(pending) == (1 if status in {"started", "dispatched", "undispatched"} else 0)
        if pending:
            assert pending[0].is_skipped == (expected_total == 0)

    def test_fetch_preserves_build_tags(self):
        api = MagicMock()
        api.build_by_id.return_value.tags = ["staging", "cloudqa"]
        api.build_by_id.return_value.get_tasks.return_value = [make_mock_task("t", "v").task]

        tasks = get_build_tasks(api, MagicMock(build_id="b1", build_variant="v"))

        assert tasks[0].build_variant_tags == ("staging", "cloudqa")


class TestWaitForCompletion:
    @pytest.mark.parametrize("final_status", ["success", "failed"])
    @patch("scripts.evergreen.notify_master_failures.time.sleep")
    @patch("scripts.evergreen.notify_master_failures.get_task_summary")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_waits_for_active_cloudqa(self, mock_version, mock_summary, mock_sleep, final_status):
        pending = make_mock_task("t", "v", "undispatched")
        pending.task.activated = True
        pending = replace(pending, build_variant_tags=("staging", "cloudqa"))
        finished = make_mock_task("t", "v", final_status)
        mock_version.return_value = make_mock_version(status="failed")
        mock_summary.side_effect = [([], [pending], [], 1), ([finished] if final_status == "failed" else [], [], [], 1)]

        wait_for_version_completion(MagicMock(), "v1")

        mock_sleep.assert_called_once()
        assert mock_summary.call_count == 2

    @patch("scripts.evergreen.notify_master_failures.time.sleep")
    @patch("scripts.evergreen.notify_master_failures.get_task_summary")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_does_not_wait_for_inactive_cloudqa_or_queued_ibm_z(self, mock_version, mock_summary, mock_sleep):
        inactive = make_mock_task("t", "v", "undispatched")
        inactive.task.activated = False
        inactive = replace(inactive, build_variant_tags=("staging", "cloudqa"))
        queued_ibm_z = make_mock_task("ibm", "e2e_smoke_ibm_z", "undispatched")
        mock_version.return_value = make_mock_version(status="started")
        mock_summary.return_value = ([], [inactive, queued_ibm_z], [], 1)

        wait_for_version_completion(MagicMock(), "v1")

        mock_sleep.assert_not_called()

    @patch("scripts.evergreen.notify_master_failures.time.time", side_effect=[0, 601])
    @patch("scripts.evergreen.notify_master_failures.time.sleep")
    @patch("scripts.evergreen.notify_master_failures.get_task_summary")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_wait_timeout_keeps_active_tasks_pending(self, mock_version, mock_summary, mock_sleep, mock_time):
        active = make_mock_task("t", "v", "undispatched")
        active.task.activated = True
        active = replace(active, build_variant_tags=("staging", "cloudqa"))
        mock_version.return_value = make_mock_version(status="started")
        mock_summary.return_value = ([], [active], [], 1)

        version, summary = wait_for_version_completion(MagicMock(), "v1")

        assert version.status == "started"
        assert summary == ([], [active], [], 1)
        mock_sleep.assert_not_called()


class TestGetPreviousVersionFailures:
    @patch("scripts.evergreen.notify_master_failures.get_task_summary")
    def test_finds_previous_version(self, mock_failed_tasks):
        """Should find the version with the highest order below current and return its failures."""
        # Simulate the raw JSON structure from recent_versions_by_project:
        # {"versions": [{"rolled_up": False, "versions": [{...}, ...]}, ...]}
        mock_api = MagicMock()
        mock_api.recent_versions_by_project.return_value.json = {
            "versions": [
                {
                    "rolled_up": False,
                    "versions": [
                        {"version_id": "v1", "order": 95},
                        {"version_id": "v2", "order": 99},  # closest below current
                        {"version_id": "v3", "order": 101},  # above current, should be skipped
                    ],
                },
            ]
        }

        prev_failed_task = make_mock_task("e2e_prev_fail", "variant_a")
        mock_failed_tasks.return_value = ([prev_failed_task], [], [], 1)

        version_info = make_mock_version(order=100)
        version_info.version.project = "my-project"

        result = get_previous_version_failures(mock_api, version_info)

        assert ("e2e_prev_fail", "variant_a") in result
        assert mock_failed_tasks.call_args[0][1] == "v2"


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


class TestWaitForVersionCompletion:
    @patch("scripts.evergreen.notify_master_failures.time.sleep")
    @patch("scripts.evergreen.notify_master_failures.get_task_summary")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_fetches_once_when_only_queued_ibm_z_remains(self, mock_get_version, mock_summary, mock_sleep):
        version_info = make_mock_version(status="started")
        mock_get_version.return_value = version_info
        queued = make_mock_task("ibm", "e2e_smoke_ibm_z", "undispatched")
        mock_summary.return_value = ([], [queued], [], 1)

        version, summary = wait_for_version_completion(MagicMock(), "v1")

        assert version is version_info
        assert summary == ([], [queued], [], 1)
        mock_get_version.assert_called_once()
        mock_sleep.assert_not_called()


class TestMainOutputBehavior:
    @pytest.fixture
    def mocks(self, monkeypatch):
        with (
            patch("scripts.evergreen.notify_master_failures.get_evergreen_api") as api,
            patch("scripts.evergreen.notify_master_failures.wait_for_version_completion") as wait,
            patch("scripts.evergreen.notify_master_failures.get_task_summary") as tasks,
            patch("scripts.evergreen.notify_master_failures.get_previous_version_failures") as prev_failed,
            patch("scripts.evergreen.notify_master_failures.send_slack_notification") as slack,
            patch("scripts.evergreen.notify_master_failures.print_stdout_report") as stdout,
        ):
            api.return_value = MagicMock()
            slack.return_value = True
            prev_failed.return_value = set()
            monkeypatch.setenv("version_id", "test")
            monkeypatch.delenv("SLACK_WEBHOOK_URL", raising=False)
            monkeypatch.delenv("NOTIFY_ON_SUCCESS", raising=False)
            yield {
                "wait": wait,
                "tasks": tasks,
                "slack": slack,
                "stdout": stdout,
                "prev_failed": prev_failed,
            }

    def _run(self, mocks, status, failed=None, running=None, retried=None, skipped=None, total=None, args=None):
        version = make_mock_version(revision="abc", status=status, author="X", author_id="x", message="m")
        # Convert dict tasks to TaskInfo if provided as dicts
        failed_tasks = [make_mock_task(t["display_name"], t["build_variant"]) for t in (failed or [])]
        pending_tasks = [make_mock_task(t["display_name"], t["build_variant"], "started") for t in (running or [])] + [
            make_mock_task(t["display_name"], t["build_variant"], "undispatched") for t in (skipped or [])
        ]
        for task, data in zip(pending_tasks, (running or []) + (skipped or [])):
            task.task.activated = data.get("activated", True)
        pending_tasks = [
            replace(task, build_variant_tags=tuple(data.get("tags", [])))
            for task, data in zip(pending_tasks, (running or []) + (skipped or []))
        ]
        retried_tasks = [
            make_mock_task(t["display_name"], t["build_variant"], "success", t["execution"]) for t in (retried or [])
        ]
        if total is None:
            total = len(failed_tasks) + len(pending_tasks) + len(retried_tasks)
        summary = (failed_tasks, pending_tasks, retried_tasks, total)
        mocks["tasks"].return_value = summary
        mocks["wait"].return_value = version, summary
        sys.argv = ["test.py"] + (args or [])
        from scripts.evergreen.notify_master_failures import main

        main()

    def test_stdout_without_webhook(self, mocks):
        self._run(mocks, "failed", failed=[{"build_variant": "v", "display_name": "e2e_t"}])
        assert mocks["stdout"].called
        assert not mocks["slack"].called

    def test_slack_reports_failures_with_pending_tasks(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(
            mocks,
            "failed",
            failed=[{"build_variant": "v", "display_name": "e2e_failed"}],
            running=[{"build_variant": "v", "display_name": "e2e_pending"}],
        )
        mocks["slack"].assert_called_once()
        message = mocks["slack"].call_args.args[1]
        assert "Build Failures (1 pending)" in message["blocks"][0]["text"]["text"]
        assert "e2e_failed" in str(message["blocks"])

    @patch("scripts.evergreen.notify_master_failures.time")
    @patch("scripts.evergreen.notify_master_failures.get_version_info")
    def test_slack_reports_active_cloudqa_at_timeout(self, mock_version, clock, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        monkeypatch.setenv("NOTIFY_ON_SUCCESS", "false")
        clock.time.side_effect = [0, 0, 601, 602]
        mock_version.return_value = make_mock_version(status="started")
        mocks["wait"].side_effect = wait_for_version_completion

        self._run(
            mocks,
            "started",
            running=[{"build_variant": "cloudqa_variant", "display_name": "e2e_task", "tags": ["staging", "cloudqa"]}],
        )

        mocks["slack"].assert_called_once()
        mocks["tasks"].assert_called_once()
        message = mocks["slack"].call_args.args[1]
        assert "Build Incomplete (1 pending)" in message["blocks"][0]["text"]["text"]

    def test_slack_for_partial_pass_with_skipped_tasks(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(
            mocks,
            "started",
            skipped=[{"build_variant": "e2e_smoke_ibm_z", "display_name": "e2e_replica_set"}],
        )
        assert mocks["slack"].called
        message = mocks["slack"].call_args.args[1]
        text = " ".join(
            block["text"]["text"] for block in message["blocks"] if block["type"] == "section" and "text" in block
        )
        assert "Skipped variants" in text
        assert "e2e_smoke_ibm_z" in text

    def test_slack_pending_count_excludes_skipped_tasks(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(
            mocks,
            "started",
            running=[{"build_variant": "v", "display_name": "e2e_t"}],
            skipped=[{"build_variant": "e2e_smoke_ibm_z", "display_name": "e2e_replica_set"}],
        )
        mocks["slack"].assert_called_once()
        message = mocks["slack"].call_args.args[1]
        assert "Build Incomplete (1 pending)" in message["blocks"][0]["text"]["text"]
        assert "Skipped variants" in str(message["blocks"])

    @pytest.mark.parametrize("activated", [False, True])
    def test_cloudqa_activation_controls_report_status(self, mocks, monkeypatch, activated):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(
            mocks,
            "started",
            skipped=[
                {
                    "build_variant": "cloudqa_variant",
                    "display_name": "e2e_task",
                    "tags": ["staging", "cloudqa"],
                    "activated": activated,
                }
            ],
        )
        mocks["slack"].assert_called_once()
        message = mocks["slack"].call_args.args[1]
        if activated:
            assert "Build Incomplete (1 pending)" in message["blocks"][0]["text"]["text"]
            assert "Skipped variants" not in str(message["blocks"])
        else:
            assert "Build Passed" in message["blocks"][0]["text"]["text"]
            assert "cloudqa_variant" in str(message["blocks"])
            assert "Skipped variants" in str(message["blocks"])

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

    def test_webhook_message_includes_capped_task_sample(self, mocks, monkeypatch):
        monkeypatch.setenv("SLACK_WEBHOOK_URL", "http://x")
        self._run(
            mocks,
            "success",
            retried=[{"build_variant": "v", "display_name": f"e2e_retried_{i}", "execution": 1} for i in range(6)],
            total=100,
        )
        message = mocks["slack"].call_args.args[1]
        text = " ".join(
            block["text"]["text"] for block in message["blocks"] if block["type"] == "section" and "text" in block
        )
        assert "6 of 100 task runs restarted before this pass" in text
        assert "and 1 more" in text
        assert "e2e_retried_5" not in text
