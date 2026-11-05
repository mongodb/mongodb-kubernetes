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
) -> report.WeeklyTask:
    return report.WeeklyTask(
        task_name=task_name,
        variant=variant,
        total_runs=successful_runs + 2,
        successful_runs=successful_runs,
        failed_runs=2,
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

    def test_shows_passed_after_retry_percent(self):
        message = format_slack_message([make_flaky_task(successful_runs=3, retried_count=1)])
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


def failed_test(
    span_id="test",
    execution="0",
    nodeid="tests/auth.py::TestDeleted::test_authentication_was_disabled",
    error="E       AssertionError: Timed out executing authentication_was_disabled after 300.5 seconds and 49 attempt(s)",
):
    return {
        "evergreen.task.id": "task-id",
        "evergreen.task.execution": execution,
        "trace.trace_id": "shared-trace",
        "trace.span_id": span_id,
        "mck.pytest.nodeid": nodeid,
        "mck.pytest.outcome.call": "failed",
        "mck.pytest.error_details": error,
        "_query_url": "https://ui.honeycomb.io/example",
    }


class TestFailureSummaries:
    def test_summarizes_non_cr_failure_and_groups_variable_poll_durations(self):
        first = failed_test()
        second = failed_test(
            execution=1,
            error="E   AssertionError: Timed out executing authentication_was_disabled after 308.0 seconds and 54 attempt(s)",
        )
        context = report._summarize_failures({"task-id": 2}, [first, first, second])
        summary = context.summaries[0]
        assert summary.nodeid.endswith("::test_authentication_was_disabled")
        assert (
            summary.message
            == "AssertionError: Timed out executing authentication_was_disabled after 300.5 seconds and 49 attempt(s)"
        )
        assert summary.error_source == "mck.pytest.error_details"
        assert summary.failed_executions == 2
        assert summary.task_runs == 1
        assert context.covered_runs == context.recovered_runs == 1
        assert summary.execution == 0

    def test_excludes_passing_expected_failure_and_later_executions(self):
        expected = failed_test(error="E   AssertionError: INVALID_ORG_ID wrongId")
        expected["mck.pytest.outcome.call"] = "passed"
        context = report._summarize_failures({"task-id": 1}, [expected, failed_test(execution=1)])
        assert not context.summaries
        assert context.covered_runs == 0

    def test_groups_wait_timeouts_and_keeps_distinct_scale_down_error(self):
        scale_up = "tests/rs.py::TestScaleUp::test_mdb_updated"
        first = failed_test(
            nodeid=scale_up,
            error="E   Exception: Timeout (700) reached while waiting for MongoDB (rs)| status: Phase.Pending| message: StatefulSet not ready",
        )
        second = failed_test(
            execution=1,
            nodeid=scale_up,
            error="E   Exception: Timeout (700) reached while waiting for MongoDB (rs)| status: Phase.Pending| message: Continuing scaling operation",
        )
        rejected = failed_test(
            nodeid="tests/rs.py::test_scale_down",
            error='E   AssertionError: Got into Failed phase while waiting for Running! ("Cannot add/remove multiple voting members of a replica set at once")',
        )
        context = report._summarize_failures({"task-id": 2}, [first, second, rejected])
        assert len(context.summaries) == 2
        assert context.summaries[0].failed_executions == 2
        assert "Timeout (700)" in context.summaries[0].message
        assert context.summaries[0].cr_example == "Pending: StatefulSet not ready"
        assert context.summaries[0].cr_source == "same pytest exception"
        assert "Cannot add/remove multiple voting members" in context.summaries[1].message

    def test_retains_setup_failures_and_missing_history_in_coverage(self):
        row = failed_test()
        row.pop("mck.pytest.outcome.call")
        row["mck.pytest.outcome.setup"] = "failed"
        missing = failed_test(execution=None)
        context = report._summarize_failures({"task-id": 1, "missing-history": 1}, [row, missing])
        assert context.covered_runs == 1
        assert context.recovered_runs == 2
        assert len(context.summaries) == 1

    def test_preserves_non_pytest_marker_exception_text(self):
        row = failed_test(error="RuntimeError: connection refused")
        context = report._summarize_failures({"task-id": 1}, [row])
        assert context.summaries[0].message == "RuntimeError: connection refused"

    def test_does_not_merge_other_errors_by_stripping_meaningful_suffixes(self):
        first = failed_test(error="E   RuntimeError: failed after 3 seconds and 2 attempt(s): certificate invalid")
        second = failed_test(
            execution=1, error="E   RuntimeError: failed after 4 seconds and 3 attempt(s): access denied"
        )
        context = report._summarize_failures({"task-id": 2}, [first, second])
        assert len(context.summaries) == 2
        assert any("certificate invalid" in summary.message for summary in context.summaries)
        assert any("access denied" in summary.message for summary in context.summaries)

    def test_uses_structured_message_on_the_failed_test_when_exception_text_is_missing(self):
        row = failed_test(error="")
        row.update({"mck.wait.last_observed.phase": "Failed", "mck.wait.last_observed.message": "Invalid config"})
        context = report._summarize_failures({"task-id": 1}, [row])
        assert context.summaries[0].message == "CR Failed: Invalid config"
        assert context.summaries[0].error_source == "mck.wait.last_observed.*"
        assert context.summaries[0].cr_source == "mck.wait.last_observed.*"


class TestCompactFailureFormatting:
    def test_two_large_summaries_fit_one_slack_section(self):
        rows = []
        task_id = "task-" + "x" * 400
        for check in ("first", "second"):
            row = failed_test(
                nodeid=f"tests/rs.py::test_{check}[" + "parameter " * 1000 + "]",
                error="E   RuntimeError: " + "failure detail " * 1000,
            )
            row.update(
                {
                    "evergreen.task.id": task_id,
                    "mck.wait.last_observed.phase": "Pending",
                    "mck.wait.last_observed.message": "CR detail " * 1000,
                    "_query_url": "https://ui.honeycomb.io/mongodb-4b/environments/production/datasets/mongodb-e2e-tests/result/example",
                }
            )
            rows.append(row)
        task = make_flaky_task(task_name="e2e_" + "task_" * 20, variant="variant_" * 15)
        task.counts_query_url = (
            "https://ui.honeycomb.io/mongodb-4b/environments/production/datasets/evergreen-agent/result/example"
        )
        task.failure_context = report._summarize_failures({task_id: 1}, rows)
        text = format_slack_message([task])["blocks"][4]["text"]["text"]
        assert len(text) <= 3000
        assert text.count("|Evergreen>") == text.count("|Honeycomb>") == 2

    def test_adds_one_observation_with_coverage_and_evidence(self):
        task = make_flaky_task()
        task.failure_context = report._summarize_failures({"task-id": 1}, [failed_test()])
        message = format_slack_message([task])
        text = message["blocks"][4]["text"]["text"]
        assert "Passed after retry: 25.0% (2 of 8 successful runs)" in text
        assert "Latest execution failed: 20.0% (2 of 10 distinct runs)" in text
        assert "Runs: 10 distinct" in text
        assert "TestDeleted::test_authentication_was_disabled" in text
        assert "Timed out executing authentication_was_disabled" in text
        assert "1 failed Evergreen attempt" in text
        assert "1 distinct run affected" in text
        assert "Example error (`mck.pytest.error_details`)" in text
        assert "after 300.5 seconds and 49 attempt(s)" in text
        assert "https://ui.honeycomb.io/example" in text
        assert "execution=0" in text
        assert "Retry score" not in text
        assert "recovered" not in str(message)

    def test_labels_cr_provenance_without_claiming_a_live_resource_read(self):
        row = failed_test(
            error="E   Exception: Timeout (600) reached while waiting for MongoDB (rs)| status: Phase.Pending| message: StatefulSet not ready"
        )
        task = make_flaky_task()
        task.failure_context = report._summarize_failures({"task-id": 1}, [row])
        text = format_slack_message([task])["blocks"][4]["text"]["text"]
        assert "CR status (same pytest exception): Pending: StatefulSet not ready" in text
        assert "Exception: Timeout (600)" in text
        assert "Example error (`mck.pytest.error_details`)" in text
        assert "CR example" not in text

    def test_missing_context_is_not_reported_as_no_cr_errors(self):
        text = format_slack_message([make_flaky_task()])["blocks"][4]["text"]["text"]
        assert "Failed-test details unavailable" in text
        assert "no CR errors" not in text

    def test_bounds_and_escapes_message_and_redacts_credentials(self):
        task = make_flaky_task()
        task.failure_context = report._summarize_failures(
            {"task-id": 1},
            [
                failed_test(
                    error="E   RuntimeError: <@everyone> token=secret mongodb://user:password@host/ " + "x" * 4000
                )
            ],
        )
        text = format_slack_message([task])["blocks"][4]["text"]["text"]
        assert "<@everyone>" not in text
        assert "&lt;@everyone&gt;" in text
        assert "secret" not in text
        assert "user:password" not in text
        assert "[REDACTED]" in text
        assert len(text) < 3000


def honeycomb_result(rows, url="https://ui.honeycomb.io/example"):
    return {"data": {"results": [{"data": row} for row in rows]}, "links": {"query_url": url}}


class TestFailureQueries:
    def test_skips_queries_when_no_retried_ids_exist(self, monkeypatch):
        monkeypatch.setattr(report, "_honeycomb_query", lambda *args: pytest.fail("Unexpected query"))
        task = make_flaky_task(retried_count=0)
        report.enrich_flaky_tasks([task], {})
        assert task.failure_context.recovered_runs == 0
        assert not task.failure_context.summaries

    def test_fetches_failed_pytest_outcomes_instead_of_wait_parent_chains(self, monkeypatch):
        queries = []

        def query(spec, dataset):
            queries.append((spec, dataset))
            match = spec["filters"][-1]
            assert dataset == report.TEST_DATASET
            if match["column"] == "evergreen.task.id":
                assert {"column": "mck.pytest.nodeid", "op": "exists"} in spec["filters"]
                assert not any(f["column"] == "mck.wait.name" for f in spec["filters"])
                return honeycomb_result([failed_test()])
            pytest.fail("Unexpected parent lookup")

        monkeypatch.setattr(report, "_honeycomb_query", query)
        task = make_flaky_task()
        window = {"start_time": 100, "end_time": 200}
        report.enrich_flaky_tasks([task], window)
        assert task.failure_context.summaries[0].nodeid.endswith("::test_authentication_was_disabled")
        assert task.failure_context.summaries[0].execution == 0
        assert len(queries) == 1
        assert all(spec["start_time"] == 100 and spec["end_time"] == 200 for spec, _ in queries)
        assert all("time_range" not in spec for spec, _ in queries)

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
    def test_only_actual_mode_sends_and_enrichment_failure_preserves_ranking(self, monkeypatch, capsys, dry_run):
        task = make_flaky_task()
        monkeypatch.setattr(report, "get_weekly_tasks", lambda window: [task])

        def fail_enrichment(*args):
            raise RuntimeError("query result limit")

        monkeypatch.setattr(report, "enrich_flaky_tasks", fail_enrichment)
        sent = []
        monkeypatch.setattr(report, "send_slack_notification", sent.append)
        monkeypatch.setattr(report.sys, "argv", ["notify_flaky_tests.py"] + (["--dry-run"] if dry_run else []))
        report.main()
        output = capsys.readouterr()
        assert "Failure details unavailable" in output.err
        assert "25.0%" in output.out
        assert all(line.count("|") == 6 for line in output.out.splitlines() if line.startswith("|"))
        assert len(sent) == (0 if dry_run else 1)
        if sent:
            assert "Failed-test details unavailable" in sent[0]["blocks"][4]["text"]["text"]

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
    def test_counts_distinct_latest_executions_with_separate_denominators(self):
        rows = [
            task_execution("first-pass", "0", "success"),
            task_execution("retried", 0, "failed"),
            task_execution("retried", "1", "success"),
            task_execution("retried", 1, "success"),
            task_execution("latest-failed", 1, "success"),
            task_execution("latest-failed", 2, "failed"),
            task_execution("running", 0, "started"),
        ]
        task = report._summarize_weekly_tasks(rows)[0]
        assert task.total_runs == 4
        assert task.successful_runs == 2
        assert task.failed_runs == 1
        assert task.other_runs == 1
        assert task.retried_runs == {"retried": 1}
        assert task.passed_after_retry_percent == 50
        assert task.latest_failed_percent == 25

    def test_ranks_the_full_population_by_true_percentage_not_average_retry_score(self):
        rows = []
        for i in range(60):
            name = f"task_{i:02}"
            rows.extend(
                [
                    task_execution(f"{name}-retry", 1, "success", name),
                    task_execution(f"{name}-first", 0, "success", name),
                ]
            )
        rows.extend(
            [
                task_execution("last-retry", 1, "success", "task_after_legacy_limit"),
                task_execution("severe-retry", 4, "success", "severe"),
                task_execution("severe-first", 0, "success", "severe"),
                task_execution("only-failed", 0, "failed", "only-failed"),
            ]
        )
        tasks = report._summarize_weekly_tasks(rows)
        assert len(tasks) == 62
        assert tasks[0].task_name == "task_after_legacy_limit"
        assert tasks[0].passed_after_retry_percent == 100
        assert all(task.passed_after_retry_percent == 50 for task in tasks[1:])
        assert all(task.task_name != "only-failed" for task in tasks)

    def test_conflicting_outcome_does_not_count_as_success_in_either_order(self):
        conflict = [task_execution("conflict", 1, "success"), task_execution("conflict", 1, "failed")]
        other = task_execution("retry", 1, "success")
        for rows in ([other, *conflict], [other, *reversed(conflict)]):
            task = report._summarize_weekly_tasks(rows)[0]
            assert task.total_runs == 2
            assert task.successful_runs == task.failed_runs == 1
            assert task.retried_runs == {"retry": 1}

    def test_failed_outcome_wins_over_in_progress_for_the_same_execution(self):
        other = task_execution("retry", 1, "success")
        records = [task_execution("conflict", 1, "failed"), task_execution("conflict", 1, "started")]
        for rows in ([other, *records], [other, *reversed(records)]):
            task = report._summarize_weekly_tasks(rows)[0]
            assert task.failed_runs == 1
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
        assert tasks[0].passed_after_retry_percent == 50
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
        assert all(task.total_runs == 1 for task in tasks)

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
