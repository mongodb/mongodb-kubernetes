#!/usr/bin/env python3
"""
Weekly flaky test notification script.

Queries Honeycomb for tasks that required retries to pass in the past 7 days
(indicating flaky behavior) and sends a summary to Slack.

Uses the same query as the Honeycomb board:
https://ui.honeycomb.io/mongodb-4b/environments/production/board/EgX7rho7iH3/kubernetes-operator

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

import requests

HONEYCOMB_API_BASE = "https://api.honeycomb.io/1"
DATASET = "evergreen-agent"
PROJECT_ID = "mongodb-kubernetes"
TIME_RANGE_SECONDS = 604800  # 7 days
HONEYCOMB_BOARD_URL = "https://ui.honeycomb.io/mongodb-4b/environments/production/board/EgX7rho7iH3/kubernetes-operator"


def _honeycomb_headers() -> dict[str, str]:
    api_key = os.environ.get("HONEYCOMB_API_KEY")
    if not api_key:
        raise RuntimeError("HONEYCOMB_API_KEY environment variable is not set")
    return {"X-Honeycomb-Team": api_key, "Content-Type": "application/json"}


def _honeycomb_query(query_spec: dict, dataset: str = DATASET) -> dict:
    headers = _honeycomb_headers()
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


@dataclass
class FlakyTask:
    task_name: str
    variant: str
    flakiness_percent: float  # AVG(mck.retry_flakiness_percent)
    total_runs: int  # COUNT of runs in the window


def get_flaky_tasks() -> list[FlakyTask]:
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

    results = _honeycomb_query(query_spec)

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


def format_slack_message(flaky_tasks: list[FlakyTask]) -> dict:
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
                        "text": "No flaky tasks found in master merges (past 7 days).",
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
                    "text": "_Retry flakiness: % of runs that needed retries to pass. Higher = more flaky._",
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
                    "text": f"*{task.task_name}*\n`{task.variant}`\nFlakiness: {task.flakiness_percent:.1f}% ({task.total_runs} runs)",
                },
            }
        )

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
    parser = argparse.ArgumentParser(description="Send weekly flaky task summary to Slack")
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print message to stdout instead of sending to Slack",
    )
    args = parser.parse_args()

    try:
        flaky_tasks = get_flaky_tasks()

        print(f"\nFound {len(flaky_tasks)} flaky tasks")

        if flaky_tasks:
            print("\n| Flakiness % | Runs | Variant | Task |")
            print("|-------------|------|---------|------|")
            for task in flaky_tasks[:5]:
                print(f"| {task.flakiness_percent:.1f}% | {task.total_runs} | {task.variant} | {task.task_name} |")

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
