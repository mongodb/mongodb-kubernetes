#!/usr/bin/env python3
"""
Slack notification script for master build failures.

This script queries the Evergreen API to find failed tasks in a build version
and sends a Slack notification to #k8s-enterprise-builds with failure details
and author information.

The script runs after all other tasks complete via Evergreen's depends_on mechanism.
It only runs on commit builds (not patches) - see .evergreen.yml for details.

Environment variables required:
- EVERGREEN_USER: Evergreen API user
- EVERGREEN_API_KEY: Evergreen API key
- SLACK_WEBHOOK_URL: Slack webhook URL for posting messages
- HONEYCOMB_API_KEY: Honeycomb API key for flakiness lookup (optional)
- version_id: The Evergreen version ID to check for failures

Usage:
    python notify_master_failures.py [--dry-run] [--version-id <id>]

Options:
    --dry-run          Print the Slack message instead of sending it
    --version-id       Override the version_id environment variable
"""

import argparse
import json
import os
import re
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass

import requests
from evergreen.api import EvergreenApi
from evergreen.task import Task
from evergreen.version import BuildVariantStatus, Version

from scripts.evergreen.notify_flaky_tests import FlakyTask, get_flaky_tasks
from scripts.python.evergreen_api import get_evergreen_api

# Terminal statuses that indicate a patch/version has completed
COMPLETED_STATUSES = {"succeeded", "failed"}

# Tasks to exclude when checking for build completion (notification/reporting tasks)
# These tasks run at the end and shouldn't block success notifications
NOTIFICATION_TASKS = [
    "notify_master_build_status",
    "notify_flaky_tests_weekly",
]

# Only report failures from e2e test tasks. Non-e2e tasks (release, preflight,
# code snippets, unit tests, etc.) should not trigger failure notifications.
E2E_TASK_PATTERN = re.compile(r"^(e2e_|test_)")

# How long to wait for the version to reach a terminal state before proceeding
# with whatever data is available (seconds). Prevents premature notifications
# when the elapsed-build-activator fires the task early.
VERSION_POLL_TIMEOUT = 600
VERSION_POLL_INTERVAL = 30

# Global counter for API calls
_api_call_count = 0


# Minimal wrappers that add missing context to evergreen.py types
@dataclass(frozen=True)
class TaskInfo:
    """Task with build_variant context (build_variant not available in evergreen.Task).

    All Task properties (display_name, status, etc.) are forwarded automatically.
    """

    task: Task
    build_variant: str

    def __getattr__(self, name: str):
        """Forward attribute access to the wrapped Task object."""
        return getattr(self.task, name)

    @property
    def is_aborted(self) -> bool:
        """Check if task was aborted (failed with 'aborted' in status details)."""
        if not self.task.status_details:
            return False
        return getattr(self.task.status_details, "desc", None) == "aborted"

    @property
    def is_failed(self) -> bool:
        """Check if task failed (excluding aborted tasks)."""
        return self.task.status in ("failed", "setup-failed") and not self.is_aborted

    @property
    def is_running(self) -> bool:
        """Check if task is currently running or pending."""
        return self.task.status in ("started", "dispatched", "undispatched")

    @property
    def is_e2e(self) -> bool:
        """Check if this is an e2e test task."""
        return bool(E2E_TASK_PATTERN.match(self.display_name))


@dataclass(frozen=True)
class VersionInfo:

    version: Version

    def __getattr__(self, name: str):
        """Forward attribute access to the wrapped Version object."""
        return getattr(self.version, name)

    @property
    def author_id(self) -> str:
        """Get author_id from version (not exposed as property in evergreen.Version)."""
        return self.version.json.get("author_id", "")

    @property
    def is_completed(self) -> bool:
        """Check if version has reached a terminal state."""
        return self.version.status in COMPLETED_STATUSES

    def get_non_successful_builds(self) -> list[BuildVariantStatus]:
        """Get build variants that are not successful (for checking failures)."""
        return [bv for bv in self.version.build_variants_status if bv.json.get("status") != "success"]


def get_version_info(api: EvergreenApi, version_id: str) -> VersionInfo:
    """Fetch version information from Evergreen API."""
    global _api_call_count
    _api_call_count += 1
    version = api.version_by_id(version_id)
    return VersionInfo(version=version)


def wait_for_version_completion(api: EvergreenApi, version_id: str) -> VersionInfo:
    """Poll the version until it reaches a terminal state or timeout.

    The elapsed-build-activator can fire this task before the build finishes.
    This loop ensures we report the final status, not an intermediate one.
    """
    deadline = time.time() + VERSION_POLL_TIMEOUT
    while True:
        version_info = get_version_info(api, version_id)
        if version_info.is_completed:
            return version_info
        remaining = deadline - time.time()
        if remaining <= 0:
            print(
                f"Version still '{version_info.status}' after {VERSION_POLL_TIMEOUT}s — "
                f"proceeding with current data",
                file=sys.stderr,
            )
            return version_info
        print(
            f"Version status is '{version_info.status}', waiting {VERSION_POLL_INTERVAL}s "
            f"for completion ({remaining:.0f}s remaining)...",
            file=sys.stderr,
        )
        time.sleep(VERSION_POLL_INTERVAL)


def get_build_tasks(api: EvergreenApi, build_variant_status: BuildVariantStatus) -> list[TaskInfo]:
    """Fetch all tasks for a build from Evergreen API."""
    global _api_call_count
    _api_call_count += 1
    build = api.build_by_id(build_variant_status.build_id)
    tasks = build.get_tasks()
    return [TaskInfo(task=task, build_variant=build_variant_status.build_variant) for task in tasks]


def get_failed_and_running_tasks(
    api: EvergreenApi, version_id: str, exclude_tasks: list[str] | None = None
) -> tuple[list[TaskInfo], list[TaskInfo]]:
    """Get failed and running tasks across all build variants for a version.

    Only e2e tasks (matching E2E_TASK_PATTERN) are included in the results.
    Non-e2e tasks (release, preflight, code snippets, etc.) are filtered out.

    Args:
        api: Evergreen API client
        version_id: Version ID to query
        exclude_tasks: List of task display names to exclude from results (e.g., notification tasks)
    Returns: (failed_tasks, running_tasks)
    """
    exclude_tasks = exclude_tasks or []
    version_info = get_version_info(api, version_id)
    failed_tasks: list[TaskInfo] = []
    running_tasks: list[TaskInfo] = []

    builds_to_check = version_info.get_non_successful_builds()

    def fetch_build_tasks(build_variant_status: BuildVariantStatus) -> tuple[list[TaskInfo], list[TaskInfo]]:
        tasks = get_build_tasks(api, build_variant_status)
        failures = [task for task in tasks if task.is_failed and task.display_name not in exclude_tasks and task.is_e2e]
        pending = [task for task in tasks if task.is_running and task.display_name not in exclude_tasks and task.is_e2e]
        return failures, pending

    with ThreadPoolExecutor(max_workers=10) as executor:
        futures = {executor.submit(fetch_build_tasks, bv): bv for bv in builds_to_check}
        for future in as_completed(futures):
            try:
                failures, running = future.result()
                failed_tasks.extend(failures)
                running_tasks.extend(running)
            except Exception as e:
                print(f"Error fetching build tasks: {e}", file=sys.stderr)

    return failed_tasks, running_tasks


MAX_DETAILED_FAILURES = 10


def _format_task_name(task: TaskInfo, flaky_map: dict[tuple[str, str], FlakyTask]) -> str:
    """Format a task display_name, appending flakiness info if known."""
    flaky = flaky_map.get((task.display_name, task.build_variant))
    if flaky is not None:
        return f"{task.display_name} *(flaky: {flaky.flakiness_percent:.0f}% over {flaky.total_runs} runs)*"
    return task.display_name


def format_slack_message(
    version_info: VersionInfo,
    failed_tasks: list[TaskInfo] | None = None,
    flaky_map: dict[tuple[str, str], FlakyTask] | None = None,
) -> dict[str, object]:
    """Format a Slack message for build status.

    Args:
        version_info: Version information
        failed_tasks: List of failed tasks
        flaky_map: Optional {(task_name, variant): FlakyTask} from Honeycomb
    """
    flaky_map = flaky_map or {}
    evergreen_version_url = f"https://spruce.mongodb.com/version/{version_info.version_id}"
    is_failure = bool(failed_tasks)
    num_failures = len(failed_tasks) if failed_tasks else 0
    run_number = getattr(version_info, "order", None)

    # <@author_id> format works with Slack's GitHub integration
    author_mention = f"<@{version_info.author_id}>" if version_info.author_id else version_info.author

    if is_failure:
        header_text = "🔴 Master Build Failures"
        button_style = "danger"
        run_prefix = f"Run #{run_number}: " if run_number else ""
        summary_text = f"{run_prefix}Master build failed: {num_failures} tasks failed for commit {version_info.revision[:8]} by {version_info.author}"
    else:
        header_text = "✅ Master Build Passed"
        button_style = None
        run_prefix = f"Run #{run_number}: " if run_number else ""
        summary_text = (
            f"{run_prefix}Master build passed for commit {version_info.revision[:8]} by {version_info.author}"
        )

    # how to format Slack messages: https://docs.slack.dev/block-kit/
    blocks = [
        {"type": "header", "text": {"type": "plain_text", "text": header_text, "emoji": True}},
        {
            "type": "section",
            "fields": [
                {"type": "mrkdwn", "text": f"*Run:*\n#{run_number}" if run_number else "*Run:*\n—"},
                {"type": "mrkdwn", "text": f"*Commit:*\n`{version_info.revision[:8]}`"},
            ],
        },
        {
            "type": "section",
            "fields": [
                {"type": "mrkdwn", "text": f"*Author:*\n{author_mention}"},
            ],
        },
        {
            "type": "section",
            "text": {"type": "mrkdwn", "text": f"*Message:*\n{version_info.message.split(chr(10))[0]}"},
        },
    ]

    # Flakiness context: how many failed tasks are known-flaky
    if is_failure and failed_tasks and flaky_map:
        flaky_failed = sum(1 for t in failed_tasks if (t.display_name, t.build_variant) in flaky_map)
        if flaky_failed > 0:
            blocks.append(
                {
                    "type": "section",
                    "text": {
                        "type": "mrkdwn",
                        "text": f"_{flaky_failed} of {num_failures} failed task(s) show flaky behavior in the past 7 days (over {min(flaky_map[(t.display_name, t.build_variant)].total_runs for t in failed_tasks if (t.display_name, t.build_variant) in flaky_map)}–{max(flaky_map[(t.display_name, t.build_variant)].total_runs for t in failed_tasks if (t.display_name, t.build_variant) in flaky_map)} runs)_",
                    },
                }
            )

    if is_failure and failed_tasks:
        failures_by_variant: dict[str, list[str]] = {}
        for task in failed_tasks:
            if task.build_variant not in failures_by_variant:
                failures_by_variant[task.build_variant] = []
            failures_by_variant[task.build_variant].append(_format_task_name(task, flaky_map))

        num_variants = len(failures_by_variant)
        blocks.append({"type": "divider"})

        if num_failures <= MAX_DETAILED_FAILURES:
            failure_lines = []
            for variant, tasks in sorted(failures_by_variant.items()):
                failure_lines.append(f"*{variant}*: {', '.join(tasks)}")
            failure_summary = "\n".join(failure_lines)
            blocks.append(
                {
                    "type": "section",
                    "text": {"type": "mrkdwn", "text": f"*Failed Tasks ({num_failures}):*\n{failure_summary}"},
                }
            )
        else:
            blocks.append(
                {
                    "type": "section",
                    "text": {
                        "type": "mrkdwn",
                        "text": f"*{num_failures} tasks failed* across {num_variants} variants\n_Click button below for details_",
                    },
                }
            )

    button = {
        "type": "button",
        "text": {"type": "plain_text", "text": "View in Evergreen", "emoji": True},
        "url": evergreen_version_url,
    }
    if button_style:
        button["style"] = button_style
    blocks.append({"type": "actions", "elements": [button]})

    return {"blocks": blocks, "text": summary_text}


def send_slack_notification(webhook_url: str, message: dict[str, object]) -> bool:
    """Send a notification to Slack."""
    try:
        response = requests.post(
            webhook_url,
            json=message,
            headers={"Content-Type": "application/json"},
            timeout=30,
        )
        response.raise_for_status()
        print("Slack notification sent successfully")
        return True
    except requests.RequestException as e:
        print(f"Failed to send Slack notification: {e}", file=sys.stderr)
        return False


def print_stdout_report(
    version_info: VersionInfo,
    failed_tasks: list[TaskInfo],
    running_tasks: list[TaskInfo],
    flaky_map: dict[tuple[str, str], FlakyTask] | None = None,
) -> None:
    """Print a concise report to stdout.

    Args:
        version_info: Version information
        failed_tasks: List of failed tasks
        running_tasks: List of running/pending tasks
        flaky_map: Optional {(task_name, variant): FlakyTask} from Honeycomb
    """
    flaky_map = flaky_map or {}
    status_str = version_info.status.upper()
    run_number = getattr(version_info, "order", None)
    run_prefix = f"Run #{run_number}: " if run_number else ""
    print(f"{run_prefix}BUILD {status_str}: {version_info.version_id}")
    print(f"Commit:  {version_info.revision[:8]} by {version_info.author}")
    print(f"Link:    https://spruce.mongodb.com/version/{version_info.version_id}")

    def print_task_table(tasks: list[TaskInfo], title: str, flaky_map: dict[tuple[str, str], FlakyTask]) -> None:
        print(f"\n{title} ({len(tasks)}):")
        print(f"{'Variant':<45} {'Task':<60}")
        print("-" * 105)
        for task in sorted(tasks, key=lambda t: (t.build_variant, t.display_name)):
            variant = task.build_variant[:44]
            display = task.display_name[:39]
            flaky = flaky_map.get((task.display_name, task.build_variant))
            if flaky is not None:
                display = f"{display} (flaky: {flaky.flakiness_percent:.0f}% over {flaky.total_runs} runs)"
            print(f"{variant:<45} {display:<60}")

    if failed_tasks:
        print_task_table(failed_tasks, "Failed Tasks", flaky_map)

    if running_tasks:
        print_task_table(running_tasks, "Pending Tasks", flaky_map)


def main() -> None:
    start_time = time.time()

    parser = argparse.ArgumentParser(description="Notify about master build failures")
    parser.add_argument("--dry-run", action="store_true", help="Print Slack JSON payload (for debugging)")
    parser.add_argument("--version-id", help="Evergreen version ID (overrides env var)")
    args = parser.parse_args()

    version_id = args.version_id or os.environ.get("version_id")
    slack_webhook_url = os.environ.get("SLACK_WEBHOOK_URL")
    notify_on_success = os.environ.get("NOTIFY_ON_SUCCESS", "true").lower() == "true"

    use_slack_dry_run = args.dry_run
    use_slack = bool(slack_webhook_url)

    if not version_id:
        print("Error: version_id not provided (use --version-id or set env var)", file=sys.stderr)
        sys.exit(1)

    print(f"Checking version: {version_id}", file=sys.stderr)

    try:
        api = get_evergreen_api()
    except RuntimeError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    # Wait for the version to reach a terminal state before reporting.
    # The elapsed-build-activator can fire this task prematurely.
    try:
        version_info = wait_for_version_completion(api, version_id)
    except Exception as e:
        print(f"Error fetching version info: {e}", file=sys.stderr)
        sys.exit(1)

    print(f"Commit: {version_info.revision[:8]} by {version_info.author}", file=sys.stderr)
    print(f"Final status: {version_info.status}", file=sys.stderr)

    try:
        failed_tasks, running_tasks = get_failed_and_running_tasks(api, version_id, NOTIFICATION_TASKS)
    except Exception as e:
        print(f"Error fetching tasks: {e}", file=sys.stderr)
        sys.exit(1)

    # Look up flakiness when there are failures (best-effort, non-fatal).
    flaky_map: dict[tuple[str, str], FlakyTask] = {}
    if failed_tasks:
        print("Looking up flakiness for failed tasks...", file=sys.stderr)
        try:
            flaky_tasks_list = get_flaky_tasks()
            flaky_map = {(t.task_name, t.variant): t for t in flaky_tasks_list}
            flaky_matches = sum(1 for t in failed_tasks if (t.display_name, t.build_variant) in flaky_map)
            print(f"  {flaky_matches} of {len(failed_tasks)} failed tasks are known-flaky", file=sys.stderr)
        except Exception as e:
            print(f"Warning: flakiness lookup failed: {e}", file=sys.stderr)

    if failed_tasks or running_tasks:
        print(f"Found {len(failed_tasks)} failed, {len(running_tasks)} running tasks", file=sys.stderr)

        if use_slack_dry_run:
            message = format_slack_message(version_info, failed_tasks, flaky_map)
            print(json.dumps(message, indent=2))
        else:
            print_stdout_report(version_info, failed_tasks, running_tasks, flaky_map)
            if use_slack and slack_webhook_url and failed_tasks:
                message = format_slack_message(version_info, failed_tasks, flaky_map)
                send_slack_notification(slack_webhook_url, message)
    else:
        print("No failed or running tasks found", file=sys.stderr)
        if notify_on_success:
            if use_slack_dry_run:
                message = format_slack_message(version_info, None)
                print(json.dumps(message, indent=2))
            else:
                print_stdout_report(version_info, [], [])
                if use_slack and slack_webhook_url:
                    message = format_slack_message(version_info, None)
                    send_slack_notification(slack_webhook_url, message)

    elapsed = time.time() - start_time
    print("\n--- Summary ---", file=sys.stderr)
    print(f"Time: {elapsed:.1f}s | API calls: {_api_call_count}", file=sys.stderr)


if __name__ == "__main__":
    main()
