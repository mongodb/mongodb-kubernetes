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
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass

import requests
from evergreen.api import EvergreenApi
from evergreen.task import Task
from evergreen.version import BuildVariantStatus, Version

from scripts.python.evergreen_api import get_evergreen_api

# Terminal statuses that indicate a patch/version has completed
COMPLETED_STATUSES = {"succeeded", "failed"}

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


def get_build_tasks(api: EvergreenApi, build_variant_status: BuildVariantStatus) -> list[TaskInfo]:
    """Fetch all tasks for a build from Evergreen API."""
    global _api_call_count
    _api_call_count += 1
    build = api.build_by_id(build_variant_status.build_id)
    tasks = build.get_tasks()
    return [TaskInfo(task=task, build_variant=build_variant_status.build_variant) for task in tasks]


def get_failed_and_running_tasks(api: EvergreenApi, version_id: str) -> tuple[list[TaskInfo], list[TaskInfo]]:
    """Get failed and running tasks across all build variants for a version.

    Optimized: Uses concurrent requests and skips successful builds.
    Returns: (failed_tasks, running_tasks)
    """
    version_info = get_version_info(api, version_id)
    failed_tasks: list[TaskInfo] = []
    running_tasks: list[TaskInfo] = []

    builds_to_check = version_info.get_non_successful_builds()

    def fetch_build_tasks(build_variant_status: BuildVariantStatus) -> tuple[list[TaskInfo], list[TaskInfo]]:
        tasks = get_build_tasks(api, build_variant_status)
        failures = [task for task in tasks if task.is_failed]
        pending = [task for task in tasks if task.is_running]
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


def format_slack_message(
    version_info: VersionInfo,
    failed_tasks: list[TaskInfo] | None = None,
) -> dict[str, object]:
    """Format a Slack message for build status.

    Args:
        version_info: Version information
        failed_tasks: List of failed tasks
    """
    evergreen_version_url = f"https://spruce.mongodb.com/version/{version_info.version_id}"
    is_failure = bool(failed_tasks)
    num_failures = len(failed_tasks) if failed_tasks else 0

    # <@author_id> format works with Slack's GitHub integration
    author_mention = f"<@{version_info.author_id}>" if version_info.author_id else version_info.author

    if is_failure:
        header_text = "🔴 Master Build Failures"
        button_style = "danger"
        summary_text = f"Master build failed: {num_failures} tasks failed for commit {version_info.revision[:8]} by {version_info.author}"
    else:
        header_text = "✅ Master Build Passed"
        button_style = None
        summary_text = f"Master build passed for commit {version_info.revision[:8]} by {version_info.author}"

    # how to format Slack messages: https://docs.slack.dev/block-kit/
    blocks = [
        {"type": "header", "text": {"type": "plain_text", "text": header_text, "emoji": True}},
        {
            "type": "section",
            "fields": [
                {"type": "mrkdwn", "text": f"*Commit:*\n`{version_info.revision[:8]}`"},
                {"type": "mrkdwn", "text": f"*Author:*\n{author_mention}"},
            ],
        },
        {
            "type": "section",
            "text": {"type": "mrkdwn", "text": f"*Message:*\n{version_info.message.split(chr(10))[0]}"},
        },
    ]

    if is_failure and failed_tasks:
        failures_by_variant: dict[str, list[str]] = {}
        for task in failed_tasks:
            if task.build_variant not in failures_by_variant:
                failures_by_variant[task.build_variant] = []
            failures_by_variant[task.build_variant].append(task.display_name)

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
) -> None:
    """Print a concise report to stdout.

    Args:
        version_info: Version information
        failed_tasks: List of failed tasks
        running_tasks: List of running/pending tasks
    """
    status_str = version_info.status.upper()
    print(f"BUILD {status_str}: {version_info.version_id}")
    print(f"Commit:  {version_info.revision[:8]} by {version_info.author}")
    print(f"Link:    https://spruce.mongodb.com/version/{version_info.version_id}")

    def print_task_table(tasks: list[TaskInfo], title: str) -> None:
        print(f"\n{title} ({len(tasks)}):")
        print(f"{'Variant':<45} {'Task':<40}")
        print("-" * 85)
        for task in sorted(tasks, key=lambda t: (t.build_variant, t.display_name)):
            variant = task.build_variant[:44]
            task_name = task.display_name[:39]
            print(f"{variant:<45} {task_name:<40}")

    if failed_tasks:
        print_task_table(failed_tasks, "Failed Tasks")

    if running_tasks:
        print_task_table(running_tasks, "Pending Tasks")


def main() -> None:
    global _api_call_count
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

    try:
        version_info = get_version_info(api, version_id)
    except Exception as e:
        print(f"Error fetching version info: {e}", file=sys.stderr)
        sys.exit(1)

    print(f"Commit: {version_info.revision[:8]} by {version_info.author}", file=sys.stderr)
    print(f"Current status: {version_info.status}", file=sys.stderr)

    try:
        failed_tasks, running_tasks = get_failed_and_running_tasks(api, version_id)
    except Exception as e:
        print(f"Error fetching tasks: {e}", file=sys.stderr)
        sys.exit(1)

    if failed_tasks or running_tasks:
        print(f"Found {len(failed_tasks)} failed, {len(running_tasks)} running tasks", file=sys.stderr)

        if use_slack_dry_run:
            message = format_slack_message(version_info, failed_tasks)
            print(json.dumps(message, indent=2))
        else:
            print_stdout_report(version_info, failed_tasks, running_tasks)
            if use_slack and slack_webhook_url and failed_tasks:
                message = format_slack_message(version_info, failed_tasks)
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
    print(f"\n--- Summary ---", file=sys.stderr)
    print(f"Time: {elapsed:.1f}s | API calls: {_api_call_count}", file=sys.stderr)


if __name__ == "__main__":
    main()
