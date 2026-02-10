#!/usr/bin/env python3
"""
Weekly flaky test notification script.

Queries Honeycomb for tests that have both passed and failed in the past 7 days
(indicating flaky behavior) and sends a summary to Slack.

Environment variables:
    HONEYCOMB_API_KEY: Honeycomb API key for authentication
    SLACK_WEBHOOK_URL: Slack webhook URL for notifications

Usage:
    python notify_flaky_tests.py [--dry-run]
"""

import argparse
import os
import sys
import time
from dataclasses import dataclass
from pprint import pprint
from typing import Any, NamedTuple

import requests

# Type alias for pytest node IDs (e.g., "tests/test_foo.py::TestClass::test_method")
NodeId = str

HONEYCOMB_API_BASE = "https://api.honeycomb.io/1"
DATASET_E2E = "mongodb-e2e-tests"  # Specific dataset for higher limits (10000)
PROJECT_ID = "mongodb-kubernetes"  # Filter to our project only
TIME_RANGE_SECONDS = 604800  # 7 days
HONEYCOMB_BOARD_URL = "https://ui.honeycomb.io/mongodb-4b/environments/production/board/EgX7rho7iH3/kubernetes-operator"


class TaskFailureInfo(NamedTuple):
    """Info about a test's failures within a specific task."""

    task_name: str
    fail_count: int


@dataclass
class FlakyTest:
    """Represents a flaky test with its statistics."""

    name: NodeId
    task: str  # Evergreen task name
    failure_count: int
    total_runs: int
    failure_rate: float
    avg_duration_ms: float  # Average task duration in milliseconds (total task time / executions)
    failing_variants: list[str] = None  # List of variant names with failures


def get_honeycomb_headers() -> dict[str, str]:
    """Get headers for Honeycomb API requests."""
    api_key = os.environ.get("HONEYCOMB_API_KEY")
    if not api_key:
        raise RuntimeError("HONEYCOMB_API_KEY environment variable is not set")
    return {
        "X-Honeycomb-Team": api_key,
        "Content-Type": "application/json",
    }


def run_query(headers: dict, query_id: str, dataset: str = DATASET_E2E) -> str:
    """Execute a Honeycomb query and return the result ID."""
    response = requests.post(
        f"{HONEYCOMB_API_BASE}/query_results/{dataset}",
        headers=headers,
        json={"query_id": query_id, "disable_series": True},
        timeout=30,
    )
    response.raise_for_status()
    return response.json()["id"]


def fetch_results(headers: dict, result_id: str, dataset: str = DATASET_E2E, max_retries: int = 10) -> dict[str, Any]:
    """Fetch query results, polling until complete."""
    for attempt in range(max_retries):
        response = requests.get(
            f"{HONEYCOMB_API_BASE}/query_results/{dataset}/{result_id}",
            headers=headers,
            timeout=30,
        )
        response.raise_for_status()
        data = response.json()

        if data.get("complete", False):
            return data

        print(f"Query not complete, waiting... (attempt {attempt + 1}/{max_retries})")
        time.sleep(2)

    raise RuntimeError("Query did not complete in time")


def create_and_run_query(headers: dict, query_spec: dict, dataset: str = DATASET_E2E) -> dict[str, Any]:
    """Create a query, run it, and return the results."""
    response = requests.post(
        f"{HONEYCOMB_API_BASE}/queries/{dataset}",
        headers=headers,
        json=query_spec,
        timeout=30,
    )
    response.raise_for_status()
    query_id = response.json()["id"]

    result_id = run_query(headers, query_id, dataset)
    return fetch_results(headers, result_id, dataset)


def get_task_avg_duration_minutes(headers: dict, task_names: list[str]) -> dict[str, float]:
    """Query Honeycomb for average task duration in minutes.

    Returns {task_name: avg_duration_minutes}.
    """
    if not task_names:
        return {}

    query_spec = {
        "time_range": TIME_RANGE_SECONDS,
        "granularity": 0,
        "breakdowns": ["evergreen.task.name"],
        "calculations": [
            {"op": "SUM", "column": "duration_ms"},
            {"op": "COUNT_DISTINCT", "column": "evergreen.task.id"},
        ],
        "filters": [
            {"column": "evergreen.project.id", "op": "=", "value": PROJECT_ID},
            {"column": "evergreen.task.name", "op": "in", "value": list(task_names)},
            {"column": "evergreen.version.requester", "op": "=", "value": "commit"},
        ],
        "orders": [{"column": "duration_ms", "op": "SUM", "order": "descending"}],
        "limit": 1000,
    }

    try:
        results = create_and_run_query(headers, query_spec)
    except requests.RequestException as e:
        print(f"Warning: Failed to query task durations: {e}")
        return {}

    durations: dict[str, float] = {}
    for row in results.get("data", {}).get("results", []):
        row_data = row.get("data", row)
        task = row_data.get("evergreen.task.name")
        sum_ms = row_data.get("SUM(duration_ms)", 0) or 0
        count = row_data.get("COUNT_DISTINCT(evergreen.task.id)", 0) or 0
        durations[task] = (sum_ms / count) / 60000  # Convert ms to minutes

    return durations


def get_failed_tests_with_tasks(headers: dict) -> dict[NodeId, TaskFailureInfo]:
    """Query for failed tests/functions with task names.

    Returns {nodeid: TaskFailureInfo(task_name, fail_count)}.
    """
    query_spec = {
        "time_range": TIME_RANGE_SECONDS,
        "granularity": 0,
        "breakdowns": ["evergreen.task.name", "mck.pytest.nodeid"],
        "calculations": [{"op": "COUNT"}],
        "filters": [
            {"column": "evergreen.project.id", "op": "=", "value": PROJECT_ID},
            {"column": "mck.pytest.nodeid", "op": "exists"},
            {
                "column": "mck.pytest.outcome.call",
                "op": "=",
                "value": "failed",
            },  # Everything related to 'mck.' is collected and sent by our automation
            {"column": "evergreen.task.name", "op": "exists"},
            {"column": "evergreen.version.requester", "op": "=", "value": "commit"},
            {"column": "evergreen.task.execution", "op": "=", "value": 0},  # First attempt only
        ],
        "orders": [{"op": "COUNT", "order": "descending"}],
        "limit": 1000,
    }

    results = create_and_run_query(headers, query_spec)

    failed_tests: dict[NodeId, TaskFailureInfo] = {}
    for row in results.get("data", {}).get("results", []):
        row_data = row.get("data", row)
        nodeid = row_data.get("mck.pytest.nodeid")
        task = row_data.get("evergreen.task.name", "unknown")
        count = row_data.get("COUNT", 0)
        if nodeid:
            # Keep entry with highest failure count for each nodeid
            if nodeid not in failed_tests or count > failed_tests[nodeid].fail_count:
                failed_tests[nodeid] = TaskFailureInfo(task_name=task, fail_count=count)

    return failed_tests


def get_failing_variants_for_tests(headers: dict, nodeids: list[NodeId]) -> dict[NodeId, list[str]]:
    """Query for which build variants have failures for each test.

    Returns {nodeid: [variant1, variant2, ...]}.
    """
    if not nodeids:
        return {}

    query_spec = {
        "time_range": TIME_RANGE_SECONDS,
        "granularity": 0,
        "breakdowns": ["mck.pytest.nodeid", "evergreen.build.name"],
        "calculations": [{"op": "COUNT"}],
        "filters": [
            {"column": "evergreen.project.id", "op": "=", "value": PROJECT_ID},
            {"column": "mck.pytest.nodeid", "op": "in", "value": list(nodeids)},
            {"column": "mck.pytest.outcome.call", "op": "=", "value": "failed"},
            {"column": "evergreen.version.requester", "op": "=", "value": "commit"},
            {"column": "evergreen.task.execution", "op": "=", "value": 0},
        ],
        "orders": [{"op": "COUNT", "order": "descending"}],
        "limit": 1000,
    }

    try:
        results = create_and_run_query(headers, query_spec)
    except requests.RequestException as e:
        print(f"Warning: Failed to query failing variants: {e}")
        return {}

    # Group variants by nodeid
    failing_variants: dict[NodeId, list[str]] = {}
    for row in results.get("data", {}).get("results", []):
        row_data = row.get("data", row)
        nodeid = row_data.get("mck.pytest.nodeid")
        variant = row_data.get("evergreen.build.name")
        failing_variants.setdefault(nodeid, []).append(variant)
    return failing_variants


def get_passed_tests_batch(headers: dict, failed_nodeids: set[NodeId]) -> dict[NodeId, int]:
    """Query for passed/successful tests. Returns {nodeid: pass_count}.

    Uses 'in' filter to query only the specific nodeids that have failures,
    ensuring we get pass counts for all failed tests within the 1000 limit.
    """
    if not failed_nodeids:
        return {}

    query_spec = {
        "time_range": TIME_RANGE_SECONDS,
        "granularity": 0,
        "breakdowns": ["mck.pytest.nodeid"],
        "calculations": [{"op": "COUNT"}],
        "filters": [
            {"column": "evergreen.project.id", "op": "=", "value": PROJECT_ID},
            {"column": "mck.pytest.nodeid", "op": "in", "value": list(failed_nodeids)},
            {"column": "mck.pytest.outcome.call", "op": "=", "value": "passed"},
            {"column": "evergreen.version.requester", "op": "=", "value": "commit"},
            {"column": "evergreen.task.execution", "op": "=", "value": 0},  # First attempt only
        ],
        "orders": [{"op": "COUNT", "order": "descending"}],
        "limit": 1000,
    }

    results = create_and_run_query(headers, query_spec, dataset=DATASET_E2E)

    passed_tests: dict[NodeId, int] = {}
    for row in results.get("data", {}).get("results", []):
        row_data = row.get("data", row)
        nodeid = row_data.get("mck.pytest.nodeid")
        count = row_data.get("COUNT", 0)
        if nodeid:
            passed_tests[nodeid] = count

    return passed_tests


def get_flaky_tests(headers: dict) -> list[FlakyTest]:
    """Query Honeycomb and return list of flaky tests above threshold."""
    # Query 1: Get all failed tests with task names
    print("Querying failed tests...")
    failed_tests = get_failed_tests_with_tasks(headers)
    print(f"  Found {len(failed_tests)} tests with failures")

    if not failed_tests:
        print("No failed tests found")
        return []

    # Query 2: Batch query for passed counts of those failed ones to identify flaky candidates
    print("Querying passed tests...")
    failed_nodeids = set(failed_tests.keys())
    passed_tests = get_passed_tests_batch(headers, failed_nodeids)
    print(f"  Found {len(passed_tests)} tests with passes")

    # Find flaky tests (tests that both pass and fail)
    print("Analyzing flaky tests...")
    flaky_candidates = []
    for nodeid, info in failed_tests.items():
        pass_count = passed_tests.get(nodeid, 0)

        # Skip if not flaky (no passes means always failing, not flaky)
        if pass_count == 0:
            continue

        # That's not accurate, since we are looking across multiple runs and not for the same run. But it should give us a good approximation.
        total_runs = pass_count + info.fail_count
        failure_rate = info.fail_count / total_runs

        flaky_candidates.append((nodeid, info.task_name, info.fail_count, total_runs, failure_rate))

    # Query Honeycomb for task durations and failing variants
    unique_tasks = list(set(task for _, task, _, _, _ in flaky_candidates))
    unique_nodeids = [nodeid for nodeid, _, _, _, _ in flaky_candidates]
    print(f"Querying task metadata for {len(unique_tasks)} tasks...")
    task_durations = get_task_avg_duration_minutes(headers, unique_tasks)
    failing_variants = get_failing_variants_for_tests(headers, unique_nodeids)

    # Build final flaky tests list
    flaky_tests = []
    for nodeid, task, fail_count, total_runs, failure_rate in flaky_candidates:
        avg_duration_min = task_durations.get(task, 0.0)  # 0.0 if not found
        test_failing_variants = failing_variants.get(nodeid, [])
        flaky_tests.append(
            FlakyTest(
                name=nodeid,
                task=task,
                failure_count=fail_count,
                total_runs=total_runs,
                failure_rate=failure_rate,
                avg_duration_ms=avg_duration_min * 60000,  # Store as ms for compatibility
                failing_variants=test_failing_variants,
            )
        )

    # Sort by failure rate descending
    flaky_tests.sort(key=lambda t: t.failure_rate, reverse=True)
    return flaky_tests


def format_test_name(full_name: str, max_length: int = 60) -> str:
    """Format test name for display, extracting just the test function name if too long."""
    if len(full_name) <= max_length:
        return full_name

    # Extract just the test function name (last part after ::)
    if "::" in full_name:
        parts = full_name.split("::")
        return parts[-1]

    return full_name[:max_length] + "..."


def format_slack_message(flaky_tests: list[FlakyTest]) -> dict:
    """Format flaky test results as Slack Block Kit message."""
    if not flaky_tests:
        return {
            "blocks": [
                {
                    "type": "header",
                    "text": {
                        "type": "plain_text",
                        "text": "Weekly Flaky Test Report",
                    },
                },
                {
                    "type": "section",
                    "text": {
                        "type": "mrkdwn",
                        "text": "No flaky tests found in master merges (past 7 days).",
                    },
                },
            ]
        }

    blocks = [
        {
            "type": "header",
            "text": {
                "type": "plain_text",
                "text": "Weekly Flaky Test Report",
            },
        },
        {
            "type": "section",
            "text": {
                "type": "mrkdwn",
                "text": "Top 5 flaky tests in master merges (past 7 days):",
            },
        },
        {
            "type": "context",
            "elements": [
                {
                    "type": "mrkdwn",
                    "text": "_Cross-run flakiness: tests that both passed and failed across different commits. This is a 7-day snapshot—some tests may have been fixed since._",
                }
            ],
        },
        {"type": "divider"},
    ]

    # Group tests into sections (Slack has limits on block count)
    max_tests_to_show = 5
    tests_to_show = flaky_tests[:max_tests_to_show]

    for test in tests_to_show:
        test_display = format_test_name(test.name)
        failure_pct = int(test.failure_rate * 100)
        avg_duration_min = test.avg_duration_ms / 60000  # Convert ms to minutes

        # Build variant info string
        variant_str = ""
        if test.failing_variants:
            variant_str = f"\nFailing on: {', '.join(sorted(test.failing_variants))}"

        # Build metadata string with duration
        duration_str = f" · {avg_duration_min:.0f} min avg" if avg_duration_min > 0 else ""

        blocks.append(
            {
                "type": "section",
                "text": {
                    "type": "mrkdwn",
                    "text": f"*{test.task}*\n`{test_display}`\nFailed {test.failure_count}/{test.total_runs} runs ({failure_pct}%){duration_str}{variant_str}",
                },
            }
        )

    # Add link to Honeycomb
    blocks.append({"type": "divider"})
    blocks.append(
        {
            "type": "context",
            "elements": [
                {
                    "type": "mrkdwn",
                    "text": f"<{HONEYCOMB_BOARD_URL}|View in Honeycomb>",
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
    parser = argparse.ArgumentParser(description="Send weekly flaky test summary to Slack")
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print message to stdout instead of sending to Slack",
    )
    args = parser.parse_args()

    try:
        headers = get_honeycomb_headers()
        flaky_tests = get_flaky_tests(headers)

        print(f"\nFound {len(flaky_tests)} flaky tests")

        message = format_slack_message(flaky_tests)

        if args.dry_run:
            print("\n--- DRY RUN: Would send this message to Slack (without slack formatting) ---")
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
