"""Tests for notify_flaky_tests.py"""

from unittest.mock import MagicMock, patch

import pytest

from scripts.evergreen import notify_flaky_tests as report
from scripts.evergreen.notify_flaky_tests import FlakyTask, _honeycomb_headers, format_slack_message


@pytest.fixture(autouse=True)
def block_unmocked_http(monkeypatch):
    def unexpected_http(*args, **kwargs):
        pytest.fail("Unexpected HTTP request in a report unit test")

    monkeypatch.setattr(report.requests, "post", unexpected_http)
    monkeypatch.setattr(report.requests, "get", unexpected_http)


def make_flaky_task(
    task_name: str = "e2e_task",
    variant: str = "e2e_variant",
    successful_runs: int = 8,
    retried_count: int = 2,
    didnt_pass_runs: int = 2,
) -> report.WeeklyTask:
    return report.WeeklyTask(
        task_name=task_name,
        variant=variant,
        total_runs=successful_runs + didnt_pass_runs,
        successful_runs=successful_runs,
        didnt_pass_runs=didnt_pass_runs,
        retried_runs={"task-id" if i == 0 else f"retry-{i}": 1 for i in range(retried_count)},
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

    def test_shows_three_outcome_counts(self):
        message = format_slack_message([make_flaky_task(successful_runs=8, retried_count=2, didnt_pass_runs=2)])
        text = message["blocks"][4]["text"]["text"]
        assert "10 runs: 6 passed without retries · 2 passed with retries · 2 didn't pass even with retries" in text

    def test_shows_other_outcomes_only_when_present(self):
        task = make_flaky_task()
        task.other_runs = 1
        text = format_slack_message([task])["blocks"][4]["text"]["text"]
        assert "· 1 other outcomes" in text
        assert "other outcomes" not in format_slack_message([make_flaky_task()])["blocks"][4]["text"]["text"]

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
            _honeycomb_headers()

    def test_returns_headers_with_key(self, monkeypatch):
        monkeypatch.setenv("HONEYCOMB_API_KEY", "test-key")
        headers = _honeycomb_headers()
        assert headers["X-Honeycomb-Team"] == "test-key"
        assert headers["Content-Type"] == "application/json"


class TestFlakyTaskDataclass:
    def test_creation(self):
        task = FlakyTask(task_name="e2e_task", variant="e2e_variant", flakiness_percent=25.0, total_runs=10)
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


def honeycomb_result(rows, url="https://ui.honeycomb.io/example"):
    return {"data": {"results": [{"data": row} for row in rows]}, "links": {"query_url": url}}


class TestQueryRows:
    def test_splits_capped_id_queries_and_keeps_each_result_link(self, monkeypatch):
        monkeypatch.setattr(report, "QUERY_RESULT_LIMIT", 2)

        def query(spec, dataset):
            values = spec["filters"][-1]["value"]
            return honeycomb_result([{"id": value} for value in values], url="https://example.com/" + values[0])

        monkeypatch.setattr(report, "_honeycomb_query", query)
        rows = report._query_rows({}, "dataset", [], ["id"], "id", ["a", "b"])
        assert [(row["id"], row["_query_url"]) for row in rows] == [
            ("a", "https://example.com/a"),
            ("b", "https://example.com/b"),
        ]

    def test_single_id_cap_fails_instead_of_reporting_partial_counts(self, monkeypatch):
        monkeypatch.setattr(report, "QUERY_RESULT_LIMIT", 2)
        monkeypatch.setattr(report, "_honeycomb_query", lambda *args: honeycomb_result([{}, {}]))
        with pytest.raises(RuntimeError, match="limit"):
            report._query_rows({}, "dataset", [], ["id"], "id", ["a"])


class TestReportModes:
    @pytest.mark.parametrize("dry_run", [True, False])
    def test_only_actual_mode_sends(self, monkeypatch, capsys, dry_run):
        task = make_flaky_task()
        monkeypatch.setattr(report, "get_weekly_tasks", lambda window: [task])
        sent = []
        monkeypatch.setattr(report, "send_slack_notification", sent.append)
        monkeypatch.setattr(report.sys, "argv", ["notify_flaky_tests.py"] + (["--dry-run"] if dry_run else []))
        report.main()
        output = capsys.readouterr()
        assert "20.0%" in output.out
        assert all(line.count("|") == 9 for line in output.out.splitlines() if line.startswith("|"))
        assert len(sent) == (0 if dry_run else 1)
        if sent:
            assert "10 runs: 6 passed without retries" in sent[0]["blocks"][4]["text"]["text"]

    def test_baseline_query_failure_does_not_send_a_healthy_report(self, monkeypatch):
        def fail_query(window):
            raise RuntimeError("Honeycomb unavailable")

        monkeypatch.setattr(report, "get_weekly_tasks", fail_query)
        sent = []
        monkeypatch.setattr(report, "send_slack_notification", sent.append)
        monkeypatch.setattr(report.sys, "argv", ["notify_flaky_tests.py"])
        with pytest.raises(SystemExit) as error:
            report.main()
        assert error.value.code == 1
        assert not sent


class TestBaselineQuery:
    def test_preserves_metric_ranking_and_adopts_returned_bounds(self, monkeypatch):
        def query(spec):
            assert spec["calculations"] == [{"op": "AVG", "column": "mck.retry_flakiness_percent"}, {"op": "COUNT"}]
            assert {"column": "evergreen.version.requester", "op": "=", "value": "gitter_request"} in spec["filters"]
            assert "time_range" not in spec
            data = honeycomb_result(
                [
                    {
                        "evergreen.task.name": "lower",
                        "evergreen.build.name": "variant",
                        "AVG(mck.retry_flakiness_percent)": 10.0,
                        "COUNT": 20,
                    },
                    {
                        "evergreen.task.name": "higher",
                        "evergreen.build.name": "variant",
                        "AVG(mck.retry_flakiness_percent)": 23.1,
                        "COUNT": 13,
                    },
                ]
            )
            data["query"] = {"start_time": 90, "end_time": 190}
            return data

        monkeypatch.setattr(report, "_honeycomb_query", query)
        window = {"start_time": 100, "end_time": 200}
        tasks = report.get_flaky_tasks(window)
        assert [(task.task_name, task.flakiness_percent, task.total_runs) for task in tasks] == [
            ("higher", 23.1, 13),
            ("lower", 10.0, 20),
        ]
        assert window == {"start_time": 90, "end_time": 190}


def task_execution(task_id, execution, status, task_name="e2e_task", variant="variant"):
    return {
        "evergreen.task.id": task_id,
        "evergreen.task.execution": execution,
        "evergreen.task.status": status,
        "evergreen.task.name": task_name,
        "evergreen.build.name": variant,
    }


class TestWeeklyRunRates:
    def test_counts_three_outcome_buckets(self):
        rows = [
            task_execution("first-pass", "0", "success"),
            task_execution("retried", 0, "failed"),
            task_execution("retried", "1", "success"),
            task_execution("retried", 1, "success"),
            task_execution("never-passed", 0, "failed"),
            task_execution("never-passed", 1, "failed"),
            task_execution("passed-then-failed", 0, "success"),
            task_execution("passed-then-failed", 1, "failed"),
            task_execution("running", 0, "started"),
        ]
        task = report._summarize_weekly_tasks(rows)[0]
        assert task.total_runs == 5
        assert task.successful_runs == 2
        assert task.passed_without_retries == 1
        assert task.passed_with_retries == 1
        assert task.didnt_pass_runs == 1
        assert task.other_runs == 2
        assert task.retried_runs == {"retried": 1}
        assert task.retried_percent == 20

    def test_ranks_the_full_population_by_retried_over_total_runs(self):
        rows = []
        for i in range(60):
            name = f"task_{i:02}"
            for j in range(9):
                rows.append(task_execution(f"{name}-first-{j}", 0, "success", name))
            rows.append(task_execution(f"{name}-retry", 1, "success", name))
        for j in range(8):
            rows.append(task_execution(f"severe-fail-{j}", 0, "failed", "severe"))
        rows.extend(
            [
                task_execution("severe-retry-0", 1, "success", "severe"),
                task_execution("severe-retry-1", 1, "success", "severe"),
                task_execution("only-failed", 0, "failed", "only-failed"),
            ]
        )
        for j in range(7):
            rows.append(task_execution(f"better-first-{j}", 0, "success", "better"))
        for j in range(3):
            rows.append(task_execution(f"better-retry-{j}", 1, "success", "better"))
        tasks = report._summarize_weekly_tasks(rows)
        assert len(tasks) == 62
        assert tasks[0].task_name == "better"
        assert tasks[0].retried_percent == 30
        assert tasks[1].task_name == "severe"
        assert tasks[1].retried_percent == 20
        assert all(task.retried_percent == 10 for task in tasks[2:])
        assert all(task.task_name != "only-failed" for task in tasks)

    def test_conflicting_outcome_does_not_count_as_success_in_either_order(self):
        conflict = [task_execution("conflict", 1, "success"), task_execution("conflict", 1, "failed")]
        other = task_execution("retry", 1, "success")
        for rows in ([other, *conflict], [other, *reversed(conflict)]):
            task = report._summarize_weekly_tasks(rows)[0]
            assert task.total_runs == 2
            assert task.successful_runs == 1
            assert task.didnt_pass_runs == 1
            assert task.retried_runs == {"retry": 1}

    def test_passed_earlier_then_failed_is_not_counted_as_never_passing(self):
        rows = [
            task_execution("passed-then-failed", 0, "success"),
            task_execution("passed-then-failed", 1, "failed"),
            task_execution("retry", 1, "success"),
        ]
        task = report._summarize_weekly_tasks(rows)[0]
        assert task.total_runs == 2
        assert task.didnt_pass_runs == 0
        assert task.other_runs == 1
        assert task.retried_runs == {"retry": 1}

    def test_failed_outcome_wins_over_in_progress_for_the_same_execution(self):
        other = task_execution("retry", 1, "success")
        records = [task_execution("conflict", 1, "failed"), task_execution("conflict", 1, "started")]
        for rows in ([other, *records], [other, *reversed(records)]):
            task = report._summarize_weekly_tasks(rows)[0]
            assert task.didnt_pass_runs == 1
            assert task.other_runs == 0

    def test_rejects_missing_identity_or_execution_in_denominator(self):
        for column in ("evergreen.task.id", "evergreen.task.execution", "evergreen.task.status"):
            row = task_execution("bad", 1, "success")
            row[column] = None
            with pytest.raises(RuntimeError, match="metadata"):
                report._summarize_weekly_tasks([row])

    def test_discovers_all_task_names_and_partitions_complete_run_metadata(self, monkeypatch):
        queries = []

        def query(spec, dataset):
            queries.append(spec)
            assert dataset == report.DATASET
            assert {"column": "name", "op": "=", "value": "task"} in spec["filters"]
            assert {"column": "evergreen.version.requester", "op": "=", "value": "gitter_request"} in spec["filters"]
            assert spec["calculations"] == [{"op": "COUNT"}]
            if spec["breakdowns"] == ["evergreen.task.name"]:
                result = honeycomb_result([{"evergreen.task.name": "e2e_task"}])
                result["query"] = {"start_time": 90, "end_time": 190}
                return result
            assert spec["start_time"] == 90 and spec["end_time"] == 190
            return honeycomb_result([task_execution("retry", 1, "success"), task_execution("first", 0, "success")])

        monkeypatch.setattr(report, "_honeycomb_query", query)
        window = {"start_time": 100, "end_time": 200}
        tasks = report.get_weekly_tasks(window)
        assert len(queries) == 2
        assert tasks[0].retried_percent == 50
        assert tasks[0].passed_without_retries == 1
        assert tasks[0].passed_with_retries == 1
        assert window == {"start_time": 90, "end_time": 190}

    def test_partitions_a_capped_task_name_by_variant(self, monkeypatch):
        monkeypatch.setattr(report, "QUERY_RESULT_LIMIT", 3)

        def query(spec, dataset):
            if spec["breakdowns"] == ["evergreen.task.name"]:
                return honeycomb_result([{"evergreen.task.name": "e2e_task"}])
            if spec["breakdowns"] == ["evergreen.build.name"]:
                return honeycomb_result([{"evergreen.build.name": "a"}, {"evergreen.build.name": "b"}])
            match = spec["filters"][-1]
            if match["column"] == "evergreen.task.name":
                return honeycomb_result([{}, {}, {}])
            assert match["column"] == "evergreen.build.name"
            return honeycomb_result(
                [task_execution(f"retry-{variant}", 1, "success", variant=variant) for variant in match["value"]]
            )

        monkeypatch.setattr(report, "_honeycomb_query", query)
        tasks = report.get_weekly_tasks({})
        assert {task.variant for task in tasks} == {"a", "b"}
        assert all(task.total_runs == 1 and task.passed_with_retries == 1 for task in tasks)

    def test_empty_metadata_cannot_be_reported_as_healthy(self, monkeypatch):
        monkeypatch.setattr(report, "_honeycomb_query", lambda *args: honeycomb_result([]))
        with pytest.raises(RuntimeError, match="metadata"):
            report.get_weekly_tasks({})

    def test_discovered_names_with_missing_records_fail_closed(self, monkeypatch):
        def query(spec, dataset):
            if spec["breakdowns"] == ["evergreen.task.name"]:
                return honeycomb_result([{"evergreen.task.name": "a"}, {"evergreen.task.name": "b"}])
            return honeycomb_result([task_execution("a", 1, "success", "a")])

        monkeypatch.setattr(report, "_honeycomb_query", query)
        with pytest.raises(RuntimeError, match="metadata"):
            report.get_weekly_tasks({})
