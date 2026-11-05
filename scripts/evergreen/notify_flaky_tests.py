#!/usr/bin/env python3
"""
Weekly flaky test notification script.

Ranks distinct task runs by the share that passed with retries in the past 7 days.
Reports first-pass, retried, and never-passed counts, then sends the failed-test
evidence from before each successful retry to Slack.

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
    failed_executions: int
    task_runs: int
    task_id: str
    execution: int
    query_url: str


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
        if not message:
            continue
        poll = re.fullmatch(
            r"([\w.]*AssertionError: Timed out executing .+?) after ([\d.]+) seconds and \d+ attempt\(s\)", message
        )
        signature = poll.group(1) if poll else message
        if re.match(r"[\w.]*Exception: Timeout \(\d+\) reached while waiting", message):
            signature = message.partition("| status:")[0].strip()
        groups[(nodeid, signature)][(task_id, execution)] = row
    # Keep the failure group with the most failed executions.
    retained = sorted(groups.items(), key=lambda item: (-len(item[1]), item[0]))[:1]
    context.covered_runs = len({task_id for _, attempts in retained for task_id, _ in attempts})
    for (nodeid, _), attempts in retained:
        task_id, execution = min(attempts)
        first_attempt = attempts[(task_id, execution)]
        context.summaries.append(
            FailureSummary(
                nodeid=nodeid,
                failed_executions=len(attempts),
                task_runs=len({key[0] for key in attempts}),
                task_id=task_id,
                execution=execution,
                query_url=first_attempt.get("_query_url", ""),
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
    didnt_pass_runs: int  # No successful execution in the report window
    retried_runs: dict[str, int]
    other_runs: int = 0
    counts_query_url: str = ""
    failure_context: FailureContext | None = None

    @property
    def passed_without_retries(self) -> int:
        return self.successful_runs - len(self.retried_runs)

    @property
    def passed_with_retries(self) -> int:
        return len(self.retried_runs)

    @property
    def retried_percent(self) -> float:
        return 100 * self.passed_with_retries / self.total_runs


def _execution(row: dict) -> int:
    value = row.get("evergreen.task.execution")
    return -1 if value is None else int(value)


def _task_statuses(rows: list[dict]) -> dict[str, dict[int, str]]:
    statuses: dict[str, dict[int, tuple[tuple[bool, bool, str], str]]] = defaultdict(dict)
    for row in rows:
        task_id = row.get("evergreen.task.id")
        execution = _execution(row)
        status = row.get("evergreen.task.status")
        if not task_id or execution < 0 or not status:
            raise RuntimeError("Incomplete task metadata prevents distinct-run counting")
        # Conflicting exports for one execution must not classify it as a success.
        priority = (status == "failed", status != "success", status)
        current = statuses[task_id].get(execution)
        if current is None or priority > current[0]:
            statuses[task_id][execution] = (priority, status)
    return {
        task_id: {execution: status for execution, (_, status) in executions.items()}
        for task_id, executions in statuses.items()
    }


def _summarize_weekly_tasks(rows: list[dict]) -> list[WeeklyTask]:
    groups = defaultdict(list)
    for row in rows:
        pair = (row.get("evergreen.task.name"), row.get("evergreen.build.name"))
        if not all(pair):
            raise RuntimeError("Incomplete task metadata prevents task/variant grouping")
        groups[pair].append(row)
    tasks = []
    for (task_name, variant), records in groups.items():
        statuses = _task_statuses(records)
        first_pass = 0
        retried = {}
        didnt_pass = 0
        other = 0
        for task_id, executions in statuses.items():
            latest_execution = max(executions)
            latest_status = executions[latest_execution]
            if latest_status == "success":
                if latest_execution == 0:
                    first_pass += 1
                else:
                    retried[task_id] = latest_execution
            elif latest_status == "failed" and "success" not in executions.values():
                didnt_pass += 1
            else:
                other += 1
        if not retried:
            continue
        tasks.append(
            WeeklyTask(
                task_name=task_name,
                variant=variant,
                total_runs=len(statuses),
                successful_runs=first_pass + len(retried),
                didnt_pass_runs=didnt_pass,
                retried_runs=retried,
                other_runs=other,
                counts_query_url=records[0].get("_query_url", ""),
            )
        )
    return sorted(
        tasks,
        key=lambda task: (
            -task.retried_percent,
            -task.passed_with_retries,
            -task.total_runs,
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
        name = shorten(_slack_text(nodeid), width=120, placeholder="…")
        task_url = (
            f"https://spruce.corp.mongodb.com/task/{quote(summary.task_id, safe='')}?execution={summary.execution}"
        )
        executions = "execution" if summary.failed_executions == 1 else "executions"
        passed_runs = "run" if context.recovered_runs == 1 else "runs"
        lines.append(
            f"↳ `{name}` — {summary.task_runs} of {context.recovered_runs} {passed_runs} that passed with retries affected · "
            f"{summary.failed_executions} failed Evergreen {executions} · "
            f"<{task_url}|Evergreen> · <{summary.query_url}|Honeycomb>"
        )
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
                        "One run = one Evergreen task ID, latest execution in the report window.\n"
                        "Passed without retries: execution 0 succeeded.\n"
                        "Passed with retries: a later execution succeeded.\n"
                        "Didn't pass even with retries: no successful execution.\n"
                        "Failure details show the failure group with the most failed executions before a successful retry."
                    ),
                }
            ],
        },
        {"type": "divider"},
    ]

    for task in flaky_tasks[:5]:
        run_label = "run" if task.total_runs == 1 else "runs"
        runs = f"<{task.counts_query_url}|{run_label}>" if task.counts_query_url else run_label
        outcomes = (
            f"{task.total_runs} {runs}: "
            f"{task.passed_without_retries} passed without retries · "
            f"{task.passed_with_retries} passed with retries · "
            f"{task.didnt_pass_runs} didn't pass even with retries"
        )
        if task.other_runs:
            outcomes += f" · {task.other_runs} other outcomes"
        blocks.append(
            {
                "type": "section",
                "text": {
                    "type": "mrkdwn",
                    "text": (
                        f"*{_slack_text(task.task_name)}*\n`{_slack_text(task.variant)}`\n"
                        f"{outcomes}\n"
                        f"{_format_failure_context(task.failure_context)}"
                    ),
                },
            }
        )

    contexts = [task.failure_context for task in flaky_tasks[:5] if task.failure_context is not None]
    coverage = (
        f"Failure details: {sum(c.covered_runs for c in contexts)}/{sum(c.recovered_runs for c in contexts)} runs that passed with retries."
        if len(contexts) == len(flaky_tasks[:5])
        else "Some failure details are unavailable."
    )
    blocks.append({"type": "divider"})
    blocks.append(
        {
            "type": "context",
            "elements": [
                {
                    "type": "mrkdwn",
                    "text": (f"<{HONEYCOMB_BOARD_URL}|View in Honeycomb> · {coverage}"),
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
            print(
                "\n| Retried/Total % | Passed without retries | Passed with retries | Didn't pass | Other | Total | Variant | Task |"
            )
            print(
                "|-----------------|------------------------|---------------------|-------------|-------|-------|---------|------|"
            )
            for task in flaky_tasks[:5]:
                print(
                    f"| {task.retried_percent:.1f}% | {task.passed_without_retries} | {task.passed_with_retries} | "
                    f"{task.didnt_pass_runs} | {task.other_runs} | {task.total_runs} | {task.variant} | {task.task_name} |"
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
