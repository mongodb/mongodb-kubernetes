#!/usr/bin/env python3
"""
Weekly flaky test notification script.

Ranks distinct task runs by the share of successful runs that passed after retry
in the past 7 days, then sends their failed-test evidence to Slack.

The master-failure notifier uses get_flaky_tasks() for the board's average retry score:
https://ui.honeycomb.io/mongodb-4b/environments/production/board/EgX7rho7iH3/kubernetes-operator

Environment variables:
    HONEYCOMB_API_KEY: Honeycomb API key for authentication
    SLACK_WEBHOOK_URL: Slack webhook URL for notifications

Usage:
    python notify_flaky_tests.py [--dry-run]
"""

import argparse
import os
import re
import sys
import time
from collections import defaultdict
from dataclasses import dataclass, field
from html import escape
from pprint import pprint
from textwrap import shorten
from urllib.parse import quote

import requests

HONEYCOMB_API_BASE = "https://api.honeycomb.io/1"
DATASET = "evergreen-agent"
TEST_DATASET = "mongodb-e2e-tests"
PROJECT_ID = "mongodb-kubernetes"
TIME_RANGE_SECONDS = 604800  # 7 days
HONEYCOMB_BOARD_URL = "https://ui.honeycomb.io/mongodb-4b/environments/production/board/EgX7rho7iH3/kubernetes-operator"
# Honeycomb returns at most 1,000 groups. Reaching that limit requires splitting or rejecting the result.
QUERY_RESULT_LIMIT = 1000
# Observed task IDs exceed 200 characters. Batches of 100 stay below Honeycomb's 100 KB request limit.
ID_BATCH_SIZE = 100
# The Query Data API permits 10 query starts per minute. A 6.1-second interval leaves boundary headroom.
QUERY_INTERVAL_SECONDS = 6.1
_last_query_started = None
SPAN_COLUMNS = [
    "evergreen.task.id",
    "evergreen.task.execution",
    "trace.trace_id",
    "trace.span_id",
]
OUTCOME_COLUMNS = [f"mck.pytest.outcome.{phase}" for phase in ("setup", "call", "teardown")]


def _honeycomb_headers() -> dict[str, str]:
    api_key = os.environ.get("HONEYCOMB_API_KEY")
    if not api_key:
        raise RuntimeError("HONEYCOMB_API_KEY environment variable is not set")
    return {"X-Honeycomb-Team": api_key, "Content-Type": "application/json"}


def _honeycomb_query(query_spec: dict, dataset: str = DATASET) -> dict:
    global _last_query_started
    headers = _honeycomb_headers()
    if _last_query_started is not None:
        time.sleep(max(0, QUERY_INTERVAL_SECONDS - (time.monotonic() - _last_query_started)))
    _last_query_started = time.monotonic()
    response = requests.post(
        f"{HONEYCOMB_API_BASE}/queries/{dataset}",
        headers=headers,
        json=query_spec,
        timeout=30,
    )
    response.raise_for_status()
    query_id = response.json()["id"]

    response = requests.post(
        f"{HONEYCOMB_API_BASE}/query_results/{dataset}",
        headers=headers,
        json={"query_id": query_id, "disable_series": True},
        timeout=30,
    )
    response.raise_for_status()
    result_id = response.json()["id"]

    for attempt in range(10):
        response = requests.get(
            f"{HONEYCOMB_API_BASE}/query_results/{dataset}/{result_id}",
            headers=headers,
            timeout=30,
        )
        response.raise_for_status()
        data = response.json()
        if data.get("complete", False):
            return data
        print(f"Query not complete, waiting... (attempt {attempt + 1}/10)")
        time.sleep(2)

    raise RuntimeError("Query did not complete in time")


def _query_rows(
    window: dict,
    dataset: str,
    filters: list[dict],
    columns: list[str],
    match_column: str | None = None,
    values: list[str] | None = None,
    fallback_column: str | None = None,
) -> list[dict]:
    if values is not None:
        if not values:
            return []
        if len(values) > ID_BATCH_SIZE:
            return [
                row
                for start in range(0, len(values), ID_BATCH_SIZE)
                for row in _query_rows(
                    window,
                    dataset,
                    filters,
                    columns,
                    match_column,
                    values[start : start + ID_BATCH_SIZE],
                    fallback_column,
                )
            ]
        filters = [*filters, {"column": match_column, "op": "in", "value": values}]
    result = _honeycomb_query(
        {
            **window,
            "granularity": 0,
            "calculations": [{"op": "COUNT"}],
            "breakdowns": columns,
            "filters": filters,
            "limit": QUERY_RESULT_LIMIT,
        },
        dataset,
    )
    rows = [row.get("data", row) for row in result.get("data", {}).get("results", [])]
    for bound in ("start_time", "end_time"):
        if bound in result.get("query", {}):
            window[bound] = result["query"][bound]
    if len(rows) >= QUERY_RESULT_LIMIT:
        if values is not None and len(values) > 1:
            midpoint = len(values) // 2
            return [
                row
                for batch in (values[:midpoint], values[midpoint:])
                for row in _query_rows(window, dataset, filters[:-1], columns, match_column, batch, fallback_column)
            ]
        if fallback_column:
            partitions = _query_rows(window, dataset, filters, [fallback_column])
            return _query_rows(
                window,
                dataset,
                filters,
                columns,
                fallback_column,
                sorted({row[fallback_column] for row in partitions}),
            )
        raise RuntimeError(f"Honeycomb result reached the {QUERY_RESULT_LIMIT}-row limit")
    return [{**row, "_query_url": result.get("links", {}).get("query_url", "")} for row in rows]


@dataclass
class FailureSummary:
    nodeid: str
    message: str
    failed_executions: int
    task_runs: int
    task_id: str
    execution: int
    query_url: str
    cr_example: str = ""
    error_source: str = "mck.pytest.error_details"
    cr_source: str = ""


@dataclass
class FailureContext:
    recovered_runs: int
    covered_runs: int = 0
    summaries: list[FailureSummary] = field(default_factory=list)


def _failure_message(details: str) -> str:
    lines = [re.sub(r"^E\s+", "", line) for line in details.splitlines() if re.match(r"^E\s+", line)]
    exceptions = [line for line in lines if re.match(r"^[\w.]*?(?:Error|Exception|Failure)(?::|$)", line)]
    if exceptions:
        return exceptions[-1].strip()
    if lines:
        return lines[0].strip()
    return next((line.strip() for line in reversed(details.splitlines()) if line.strip()), "")


def _summarize_failures(recovered: dict[str, int], rows: list[dict]) -> FailureContext:
    context = FailureContext(recovered_runs=len(recovered))
    groups = defaultdict(dict)
    for row in rows:
        task_id, execution = row.get("evergreen.task.id"), _execution(row)
        nodeid = row.get("mck.pytest.nodeid")
        if (
            not task_id
            or not nodeid
            or execution < 0
            or execution >= recovered.get(task_id, -1)
            or not any(row.get(column) == "failed" for column in OUTCOME_COLUMNS)
        ):
            continue
        message = _failure_message(row.get("mck.pytest.error_details") or "")
        error_source = "mck.pytest.error_details"
        if not message and row.get("mck.wait.last_observed.message"):
            error_source = "mck.wait.last_observed.*"
            message = (
                f"CR {row.get('mck.wait.last_observed.phase') or 'Unknown'}: {row['mck.wait.last_observed.message']}"
            )
        if not message:
            continue
        poll = re.fullmatch(
            r"([\w.]*AssertionError: Timed out executing .+?) after ([\d.]+) seconds and \d+ attempt\(s\)", message
        )
        signature = poll.group(1) if poll else message
        if re.match(r"[\w.]*Exception: Timeout \(\d+\) reached while waiting", message):
            signature = message.partition("| status:")[0].strip()
        groups[(nodeid, signature)][(task_id, execution)] = {
            **row,
            "_failure_text": message,
            "_error_source": error_source,
        }
    context.covered_runs = len({task_id for attempts in groups.values() for task_id, _ in attempts})
    # The ranked replica-set task has two distinct failed checks. Keep both within the five-task layout.
    for (nodeid, _), attempts in sorted(groups.items(), key=lambda item: (-len(item[1]), item[0]))[:2]:
        task_id, execution = min(attempts)
        example = attempts[(task_id, execution)]
        cr_example = example.get("mck.wait.last_observed.message") or ""
        cr_source = ""
        if cr_example:
            cr_source = "mck.wait.last_observed.*"
            cr_example = f"{example.get('mck.wait.last_observed.phase') or 'Unknown'}: {cr_example}"
        elif "| status:" in example["_failure_text"]:
            cr_source = "same pytest exception"
            cr_example = example["_failure_text"].partition("| status:")[2].strip()
            cr_example = re.sub(r" after \d+ attempt\(s\) \(last observed phase:.*$", "", cr_example)
            cr_example = re.sub(r"^Phase\.(\w+)\|\s*message:\s*", r"\1: ", cr_example)
        context.summaries.append(
            FailureSummary(
                nodeid=nodeid,
                message=example["_failure_text"],
                failed_executions=len(attempts),
                task_runs=len({key[0] for key in attempts}),
                task_id=task_id,
                execution=execution,
                query_url=example.get("_query_url", ""),
                cr_example=cr_example,
                error_source=example["_error_source"],
                cr_source=cr_source,
            )
        )
    return context


@dataclass
class FlakyTask:
    task_name: str
    variant: str
    flakiness_percent: float  # AVG(mck.retry_flakiness_percent)
    total_runs: int  # COUNT of runs in the window


@dataclass
class WeeklyTask:
    task_name: str
    variant: str
    total_runs: int
    successful_runs: int
    failed_runs: int
    retried_runs: dict[str, int]
    counts_query_url: str = ""
    failure_context: FailureContext | None = None

    @property
    def passed_after_retry_percent(self) -> float:
        return 100 * len(self.retried_runs) / self.successful_runs

    @property
    def latest_failed_percent(self) -> float:
        return 100 * self.failed_runs / self.total_runs

    @property
    def other_runs(self) -> int:
        return self.total_runs - self.successful_runs - self.failed_runs


def _execution(row: dict) -> int:
    value = row.get("evergreen.task.execution")
    return -1 if value is None else int(value)


def _latest_runs(rows: list[dict]) -> dict[str, dict]:
    latest = {}
    for row in rows:
        task_id = row.get("evergreen.task.id")
        execution = _execution(row)
        if not task_id or execution < 0 or not row.get("evergreen.task.status"):
            raise RuntimeError("Incomplete task metadata prevents distinct-run counting")
        previous = latest.get(task_id, {})
        status = row["evergreen.task.status"]
        last_status = previous.get("evergreen.task.status", "")
        # Conflicting exports for one execution must not classify it as a success.
        priority = (execution, status == "failed", status != "success", status)
        last_priority = (_execution(previous), last_status == "failed", last_status != "success", last_status)
        if priority > last_priority:
            latest[task_id] = row
    return latest


def _summarize_weekly_tasks(rows: list[dict]) -> list[WeeklyTask]:
    groups = defaultdict(list)
    for row in rows:
        pair = (row.get("evergreen.task.name"), row.get("evergreen.build.name"))
        if not all(pair):
            raise RuntimeError("Incomplete task metadata prevents task/variant grouping")
        groups[pair].append(row)
    tasks = []
    for (task_name, variant), records in groups.items():
        latest = _latest_runs(records)
        successful = {key: row for key, row in latest.items() if row["evergreen.task.status"] == "success"}
        retried = {key: _execution(row) for key, row in successful.items() if _execution(row) > 0}
        if not retried:
            continue
        tasks.append(
            WeeklyTask(
                task_name=task_name,
                variant=variant,
                total_runs=len(latest),
                successful_runs=len(successful),
                failed_runs=sum(row["evergreen.task.status"] == "failed" for row in latest.values()),
                retried_runs=retried,
                counts_query_url=records[0].get("_query_url", ""),
            )
        )
    return sorted(
        tasks,
        key=lambda task: (
            -task.passed_after_retry_percent,
            -len(task.retried_runs),
            -task.successful_runs,
            task.task_name,
            task.variant,
        ),
    )


def get_weekly_tasks(window: dict) -> list[WeeklyTask]:
    print("Querying distinct weekly task runs from evergreen-agent...", file=sys.stderr)
    filters = [
        {"column": "name", "op": "=", "value": "task"},
        {"column": "evergreen.project.id", "op": "=", "value": PROJECT_ID},
        {"column": "evergreen.version.requester", "op": "=", "value": "gitter_request"},
        {"column": "evergreen.task.name", "op": "!=", "value": "lint_repo"},
    ]
    names = _query_rows(window, DATASET, filters, ["evergreen.task.name"])
    task_names = sorted({row["evergreen.task.name"] for row in names})
    if not task_names:
        raise RuntimeError("No weekly task metadata returned; refusing to send an empty report")
    rows = _query_rows(
        window,
        DATASET,
        filters,
        [
            "evergreen.task.id",
            "evergreen.task.execution",
            "evergreen.task.status",
            "evergreen.task.name",
            "evergreen.build.name",
        ],
        "evergreen.task.name",
        task_names,
        fallback_column="evergreen.build.name",
    )
    if set(task_names) != {row.get("evergreen.task.name") for row in rows}:
        raise RuntimeError("Incomplete task metadata prevents complete weekly ranking")
    tasks = _summarize_weekly_tasks(rows)
    print(f"  Found {len(tasks)} task/variant combinations with successful retries", file=sys.stderr)
    return tasks


def enrich_flaky_tasks(flaky_tasks: list[WeeklyTask], window: dict) -> None:
    for task in flaky_tasks[:5]:
        print(f"Querying failed tests for {task.variant}/{task.task_name}...", file=sys.stderr)
        failures = _query_rows(
            window,
            TEST_DATASET,
            [
                {"column": "status_code", "op": "=", "value": 2},
                {"column": "mck.pytest.nodeid", "op": "exists"},
            ],
            [
                *SPAN_COLUMNS,
                *OUTCOME_COLUMNS,
                "mck.pytest.nodeid",
                "mck.pytest.error_details",
                "mck.wait.last_observed.phase",
                "mck.wait.last_observed.message",
            ],
            "evergreen.task.id",
            sorted(task.retried_runs),
        )
        task.failure_context = _summarize_failures(task.retried_runs, failures)


def _slack_text(text: str) -> str:
    text = re.sub(r"(?i)(mongodb(?:\+srv)?://)[^\s/@]+@", r"\1[REDACTED]@", text)
    text = re.sub(
        r"(?i)\b(password|passwd|token|api[_-]?key|authorization)([\"']?\s*[:=]\s*)"
        r"(?:Bearer\s+)?(?:\"[^\"]*\"|'[^']*'|[^\s,;]+)",
        r"\1\2[REDACTED]",
        text,
    )
    return escape(" ".join(text.split()).replace("`", "'"), quote=False)


def _format_failure_context(context: FailureContext | None) -> str:
    if context is None or not context.summaries:
        return "↳ Failed-test details unavailable"
    lines = []
    for summary in context.summaries:
        nodeid = summary.nodeid.partition("::")[2] or summary.nodeid
        # Messages exceed 1,000 characters. These excerpts keep two examples with 400-character task IDs below Slack's 3,000-character section limit.
        name = shorten(_slack_text(nodeid), width=120, placeholder="…")
        message = shorten(_slack_text(summary.message), width=280, placeholder="…")
        task_url = (
            f"https://spruce.corp.mongodb.com/task/{quote(summary.task_id, safe='')}?execution={summary.execution}"
        )
        attempts = "attempt" if summary.failed_executions == 1 else "attempts"
        runs = "run" if summary.task_runs == 1 else "runs"
        lines.append(
            f"↳ `{name}`\n{summary.task_runs} distinct {runs} affected · "
            f"{summary.failed_executions} failed Evergreen {attempts}\n"
            f"Example error (`{summary.error_source}`): `{message}`"
        )
        if summary.cr_example:
            lines.append(
                f"CR status ({summary.cr_source}): {shorten(_slack_text(summary.cr_example), width=100, placeholder='…')}"
            )
        lines.append(f"<{task_url}|Evergreen> · <{summary.query_url}|Honeycomb>")
    return "\n".join(lines)


def get_flaky_tasks(window: dict | None = None) -> list[FlakyTask]:
    """Query Honeycomb for flaky tasks (past 7 days, retry_flakiness_percent > 0).

    Self-contained: fetches its own Honeycomb auth. Raises RuntimeError if
    HONEYCOMB_API_KEY is not set.
    """
    print("Querying flaky tasks from evergreen-agent...", file=sys.stderr)

    query_spec = {
        "time_range": TIME_RANGE_SECONDS,
        "granularity": 0,
        "breakdowns": ["evergreen.task.name", "evergreen.build.name"],
        "calculations": [
            {"op": "AVG", "column": "mck.retry_flakiness_percent"},
            {"op": "COUNT"},
        ],
        "filters": [
            {"column": "evergreen.task.status", "op": "exists"},
            {"column": "evergreen.version.requester", "op": "=", "value": "gitter_request"},
            {"column": "evergreen.project.id", "op": "=", "value": PROJECT_ID},
        ],
        "havings": [{"calculate_op": "AVG", "column": "mck.retry_flakiness_percent", "op": ">", "value": 0}],
        "orders": [{"column": "mck.retry_flakiness_percent", "op": "AVG", "order": "descending"}],
        "limit": 50,
    }
    if window is not None:
        query_spec.pop("time_range")
        query_spec.update(window)

    results = _honeycomb_query(query_spec)
    if window is not None:
        for bound in ("start_time", "end_time"):
            window[bound] = results.get("query", {}).get(bound, window[bound])

    flaky_tasks = []
    for row in results.get("data", {}).get("results", []):
        row_data = row.get("data", row)
        task_name = row_data.get("evergreen.task.name")
        variant = row_data.get("evergreen.build.name")
        flakiness = row_data.get("AVG(mck.retry_flakiness_percent)", 0)

        if task_name in ("OTHER", "TOTAL") or variant in ("OTHER", "TOTAL"):
            continue
        if task_name and variant and flakiness > 0:
            total_runs = int(row_data.get("COUNT", 0) or 0)
            flaky_tasks.append(
                FlakyTask(
                    task_name=task_name,
                    variant=variant,
                    flakiness_percent=flakiness,
                    total_runs=total_runs,
                )
            )

    flaky_tasks.sort(key=lambda t: (t.flakiness_percent, t.total_runs), reverse=True)
    print(f"  Found {len(flaky_tasks)} flaky task/variant combinations", file=sys.stderr)
    return flaky_tasks


def format_slack_message(flaky_tasks: list[WeeklyTask]) -> dict:
    """Format flaky task results as Slack Block Kit message."""
    if not flaky_tasks:
        return {
            "blocks": [
                {
                    "type": "header",
                    "text": {"type": "plain_text", "text": "Weekly Flaky Task Report"},
                },
                {
                    "type": "section",
                    "text": {
                        "type": "mrkdwn",
                        "text": "No flaky tasks found with successful retries in master merges (past 7 days).",
                    },
                },
            ]
        }

    blocks = [
        {
            "type": "header",
            "text": {"type": "plain_text", "text": "Weekly Flaky Task Report"},
        },
        {
            "type": "section",
            "text": {
                "type": "mrkdwn",
                "text": "Top 5 flaky tasks in master merges (past 7 days):",
            },
        },
        {
            "type": "context",
            "elements": [
                {
                    "type": "mrkdwn",
                    "text": (
                        "One run = one Evergreen task ID, using its latest recorded execution in the report window. "
                        "Passed after retry = retried successes / all successes. "
                        "Latest execution failed = latest failures / all distinct runs."
                    ),
                }
            ],
        },
        {"type": "divider"},
    ]

    for task in flaky_tasks[:5]:
        blocks.append(
            {
                "type": "section",
                "text": {
                    "type": "mrkdwn",
                    "text": (
                        f"*{_slack_text(task.task_name)}*\n`{_slack_text(task.variant)}`\n"
                        f"Passed after retry: {task.passed_after_retry_percent:.1f}% "
                        f"({len(task.retried_runs)} of {task.successful_runs} successful runs)\n"
                        f"Latest execution failed: {task.latest_failed_percent:.1f}% "
                        f"({task.failed_runs} of {task.total_runs} distinct runs)\n"
                        f"Runs: {task.total_runs} distinct · {task.successful_runs} passed · {task.failed_runs} latest failed"
                        f" · {task.other_runs} other outcomes\n"
                        + (f"<{task.counts_query_url}|Run counts>\n" if task.counts_query_url else "")
                        + "Earlier failures in runs that passed after retry:\n"
                        f"{_format_failure_context(task.failure_context)}"
                    ),
                },
            }
        )

    contexts = [task.failure_context for task in flaky_tasks[:5] if task.failure_context is not None]
    coverage = (
        f"Failure details: {sum(c.covered_runs for c in contexts)}/{sum(c.recovered_runs for c in contexts)} runs that passed after retry. "
        if len(contexts) == len(flaky_tasks[:5])
        else "Some failure details are unavailable. "
    )
    blocks.append({"type": "divider"})
    blocks.append(
        {
            "type": "context",
            "elements": [
                {
                    "type": "mrkdwn",
                    "text": (
                        f"<{HONEYCOMB_BOARD_URL}|View in Honeycomb> · "
                        f"{coverage}A run can appear under multiple checks. "
                        "Polling attempts inside exception text are not Evergreen executions. "
                        "Latest-failed runs' errors are outside this breakdown. Error examples are not confirmed root causes."
                    ),
                }
            ],
        }
    )

    return {"blocks": blocks}


def send_slack_notification(message: dict) -> None:
    """Send formatted message to Slack webhook."""
    webhook_url = os.environ.get("SLACK_WEBHOOK_URL")
    if not webhook_url:
        raise RuntimeError("SLACK_WEBHOOK_URL environment variable is not set")

    response = requests.post(
        webhook_url,
        json=message,
        headers={"Content-Type": "application/json"},
        timeout=30,
    )
    response.raise_for_status()
    print("Slack notification sent successfully")


def main():
    parser = argparse.ArgumentParser(description="Send weekly flaky task summary to Slack")
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print message to stdout instead of sending to Slack",
    )
    args = parser.parse_args()

    try:
        end_time = int(time.time())
        window = {"start_time": end_time - TIME_RANGE_SECONDS, "end_time": end_time}
        flaky_tasks = get_weekly_tasks(window)
        try:
            enrich_flaky_tasks(flaky_tasks, window)
        except (requests.RequestException, RuntimeError, ValueError) as e:
            print(f"Failure details unavailable: {e}", file=sys.stderr)

        print(f"\nFound {len(flaky_tasks)} flaky tasks")

        if flaky_tasks:
            print("\n| Passed after retry % | Latest execution failed % | Distinct runs | Variant | Task |")
            print("|----------------------|---------------------------|---------------|---------|------|")
            for task in flaky_tasks[:5]:
                print(
                    f"| {task.passed_after_retry_percent:.1f}% | {task.latest_failed_percent:.1f}% | "
                    f"{task.total_runs} | {task.variant} | {task.task_name} |"
                )

        message = format_slack_message(flaky_tasks)

        if args.dry_run:
            print("\n--- DRY RUN: Would send this message to Slack ---")
            pprint(message)
        else:
            send_slack_notification(message)

    except requests.RequestException as e:
        print(f"HTTP request failed: {e}", file=sys.stderr)
        sys.exit(1)
    except RuntimeError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
