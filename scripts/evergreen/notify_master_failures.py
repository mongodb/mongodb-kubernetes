#!/usr/bin/env python3
"""
Slack notification script for master or backporting-branch build failures.

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

Optional Slack bot-token variables (needed to post the retry follow-up in a thread):
- SLACK_BOT_TOKEN: Slack bot token with chat:write
- SLACK_CHANNEL_ID: Channel ID to post to

Without the bot token, the script posts the retry count through the webhook. The
restarted task list stays in the task log.

Usage:
    python notify_master_failures.py [--dry-run] [--version-id <id>]

Options:
    --dry-run          Print the Slack payloads instead of sending them
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

# How long to wait for the version to reach a terminal state before proceeding
# with whatever data is available (seconds). Prevents premature notifications
# when the elapsed-build-activator fires the task early.
VERSION_POLL_TIMEOUT = 600
VERSION_POLL_INTERVAL = 30

# Slack chat.postMessage endpoint. The webhook cannot return a message ts, so a
# bot token is required to post a threaded follow-up.
SLACK_API_URL = "https://slack.com/api/chat.postMessage"

# Retry details are capped because a full-version restart marks every task as
# restarted. Slack also limits a section text to 3000 characters.
MAX_RETRY_DETAILS = 50
MAX_RETRY_SECTION_CHARS = 2800

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
    def is_display_only(self) -> bool:
        """Check if task is an aggregate display task."""
        return bool(self.task.display_only)

    @property
    def is_retried(self) -> bool:
        """Check if the task ran more than once.

        execution is the 0-based index of the current run, so 1 means one restart.
        """
        return (self.task.execution or 0) > 0


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


def get_task_summary(
    api: EvergreenApi, version_id: str, exclude_tasks: list[str] | None = None
) -> tuple[list[TaskInfo], list[TaskInfo], list[TaskInfo], int]:
    """Get failed, running, and restarted tasks across all build variants for a version.

    All builds are fetched because the version API returns no build status, and a
    passing version must report the tasks that restarted before the pass.

    Args:
        api: Evergreen API client
        version_id: Version ID to query
        exclude_tasks: List of task display names to exclude from results (e.g., notification tasks)
    Returns: (failed_tasks, running_tasks, retried_tasks, total_tasks)
    """
    exclude_tasks = exclude_tasks or []
    version_info = get_version_info(api, version_id)
    failed_tasks: list[TaskInfo] = []
    running_tasks: list[TaskInfo] = []
    retried_tasks: list[TaskInfo] = []
    total_tasks = 0

    builds_to_check = version_info.build_variants_status

    def fetch_build_tasks(
        build_variant_status: BuildVariantStatus,
    ) -> tuple[list[TaskInfo], list[TaskInfo], list[TaskInfo], int]:
        tasks = get_build_tasks(api, build_variant_status)
        visible = [task for task in tasks if not task.is_display_only and task.display_name not in exclude_tasks]
        failures = [task for task in visible if task.is_failed]
        pending = [task for task in visible if task.is_running]
        retried = [task for task in visible if task.is_retried]
        return failures, pending, retried, len(visible)

    with ThreadPoolExecutor(max_workers=10) as executor:
        futures = {executor.submit(fetch_build_tasks, bv): bv for bv in builds_to_check}
        for future in as_completed(futures):
            try:
                failures, running, retried, num_tasks = future.result()
                failed_tasks.extend(failures)
                running_tasks.extend(running)
                retried_tasks.extend(retried)
                total_tasks += num_tasks
            except Exception as e:
                print(f"Error fetching build tasks: {e}", file=sys.stderr)

    return failed_tasks, running_tasks, retried_tasks, total_tasks


MAX_DETAILED_FAILURES = 10


def get_previous_version_failures(api: EvergreenApi, version_info: VersionInfo) -> set[tuple[str, str]]:
    """Get the set of (display_name, build_variant) tuples that failed on the previous master version.

    Returns an empty set if no previous version is found or if the lookup fails.
    """
    current_order = getattr(version_info, "order", None)
    project_id = getattr(version_info, "project", None)
    if current_order is None or project_id is None:
        return set()

    global _api_call_count
    _api_call_count += 1
    recent = api.recent_versions_by_project(project_id, params={"limit": "50"})

    # Work around evergreen.py bug: RecentVersions.versions passes a list to
    # Version() instead of individual dicts, so all fields are None.
    # Access the raw JSON directly.
    all_versions = [
        v
        for wrapper in recent.json.get("versions", [])
        for v in wrapper.get("versions", [])
        if v.get("order") is not None
    ]

    candidates = [v for v in all_versions if v["order"] < current_order]
    if not candidates:
        print("No previous master version found", file=sys.stderr)
        return set()

    prev_version_id = max(candidates, key=lambda v: v["order"])["version_id"]
    print(f"Checking previous master version: {prev_version_id}", file=sys.stderr)
    prev_failed, _, _, _ = get_task_summary(api, prev_version_id, NOTIFICATION_TASKS)
    return {(t.display_name, t.build_variant) for t in prev_failed}


def _format_task_name(
    task: TaskInfo, flaky_map: dict[tuple[str, str], FlakyTask], prev_failed: set[tuple[str, str]] | None = None
) -> str:
    """Format a task display_name, appending flakiness and/or previous-master info if known."""
    prev_failed = prev_failed or set()
    annotations = []
    flaky = flaky_map.get((task.display_name, task.build_variant))
    if flaky is not None:
        annotations.append(f"flaky: {flaky.flakiness_percent:.0f}% over {flaky.total_runs} runs")
    if (task.display_name, task.build_variant) in prev_failed:
        annotations.append("also on previous master")
    if annotations:
        return f"{task.display_name} *({' | '.join(annotations)})*"
    return task.display_name


MAX_RETRY_TASK_NAMES = 3


def _format_retry_tasks(retried_tasks: list[TaskInfo]) -> str:
    """Summarize the tasks that restarted, capped for a short message.

    Task names repeat across variants. The list is deduplicated so a task that
    restarted in three variants appears once.
    """
    names = sorted({task.display_name for task in retried_tasks})
    shown = names[:MAX_RETRY_TASK_NAMES]
    hidden = len(names) - len(shown)
    text = f"Tasks: {', '.join(shown)}"
    if hidden:
        text += f" and {hidden} more"
    return text


def _retry_detail_sections(retried_tasks: list[TaskInfo]) -> list[dict[str, object]]:
    """Build section blocks that list restarted tasks, grouped by build variant.

    Slack caps a section text at 3000 characters. Long lists are split across
    sections and capped at MAX_RETRY_DETAILS entries.
    """
    sorted_tasks = sorted(retried_tasks, key=lambda t: (t.build_variant, t.display_name))
    shown = sorted_tasks[:MAX_RETRY_DETAILS]
    hidden = len(sorted_tasks) - len(shown)

    by_variant: dict[str, list[str]] = {}
    for task in shown:
        by_variant.setdefault(task.build_variant, []).append(task.display_name)

    # A variant can hold enough tasks to exceed one section. Repeat the variant
    # prefix on continuation lines so no single line passes the section limit.
    lines: list[str] = []
    for variant, task_strings in sorted(by_variant.items()):
        prefix = f"*{variant}*: "
        chunk: list[str] = []
        for task_string in task_strings:
            if chunk and len(prefix + ", ".join(chunk + [task_string])) > MAX_RETRY_SECTION_CHARS:
                lines.append(prefix + ", ".join(chunk))
                chunk = [task_string]
            else:
                chunk.append(task_string)
        lines.append(prefix + ", ".join(chunk))
    if hidden:
        lines.append(f"_and {hidden} more_")

    sections: list[dict[str, object]] = []
    chunk: list[str] = []
    for line in lines:
        if chunk and len("\n".join(chunk + [line])) > MAX_RETRY_SECTION_CHARS:
            sections.append({"type": "section", "text": {"type": "mrkdwn", "text": "\n".join(chunk)}})
            chunk = []
        chunk.append(line)
    if chunk:
        sections.append({"type": "section", "text": {"type": "mrkdwn", "text": "\n".join(chunk)}})
    return sections


def format_retry_follow_up(retried_tasks: list[TaskInfo], total_tasks: int | None = None) -> dict[str, object] | None:
    """Format the threaded follow-up that lists restarted tasks.

    Returns None when no task restarted.
    """
    if not retried_tasks:
        return None
    if total_tasks:
        summary = f"{len(retried_tasks)} of {total_tasks} tasks restarted before this pass"
    else:
        summary = f"{len(retried_tasks)} tasks restarted before this pass"
    return {
        "blocks": [
            {"type": "section", "text": {"type": "mrkdwn", "text": f"*{summary}:*"}},
            *_retry_detail_sections(retried_tasks),
        ],
        "text": summary,
    }


def format_slack_message(
    version_info: VersionInfo,
    failed_tasks: list[TaskInfo] | None = None,
    flaky_map: dict[tuple[str, str], FlakyTask] | None = None,
    prev_failed: set[tuple[str, str]] | None = None,
    retried_tasks: list[TaskInfo] | None = None,
    total_tasks: int | None = None,
) -> dict[str, object]:
    """Format a Slack message for build status.

    Args:
        version_info: Version information
        failed_tasks: List of failed tasks
        flaky_map: Optional {(task_name, variant): FlakyTask} from Honeycomb
        prev_failed: Optional set of (task_name, variant) that also failed on the previous master
        retried_tasks: Tasks that ran more than once, reported on passing builds
        total_tasks: Total task count, used as the denominator for the retry count
    """
    flaky_map = flaky_map or {}
    prev_failed = prev_failed or set()
    retried_tasks = retried_tasks or []
    evergreen_version_url = f"https://spruce.mongodb.com/version/{version_info.version_id}"
    is_failure = bool(failed_tasks)
    num_failures = len(failed_tasks) if failed_tasks else 0
    run_number = getattr(version_info, "order", None)
    num_retried = len(retried_tasks)
    retry_summary = ""
    if not is_failure and total_tasks:
        if num_retried:
            retry_summary = f"{num_retried} of {total_tasks} tasks restarted before this pass"
        else:
            retry_summary = "no tasks restarted"

    # <@author_id> format works with Slack's GitHub integration
    author_mention = f"<@{version_info.author_id}>" if version_info.author_id else version_info.author

    branch = getattr(version_info, "branch", None) or "master"
    branch_label = "Master" if branch == "master" else branch

    if is_failure:
        header_text = f"🔴 {branch_label} Build Failures"
        button_style = "danger"
        run_prefix = f"Run #{run_number}: " if run_number else ""
        summary_text = f"{run_prefix}{branch_label} build failed: {num_failures} tasks failed for commit {version_info.revision[:8]} by {version_info.author}"
    else:
        header_text = f"✅ {branch_label} Build Passed"
        button_style = None
        run_prefix = f"Run #{run_number}: " if run_number else ""
        summary_text = (
            f"{run_prefix}{branch_label} build passed for commit {version_info.revision[:8]} by {version_info.author}"
        )
        if retry_summary:
            summary_text = f"{summary_text} ({retry_summary})"

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

    # Retry context appears only on passing builds. On failures the retry count is
    # not the reason the message was sent.
    if retry_summary:
        retry_text = f"_{retry_summary}_"
        if num_retried:
            retry_text += f"\n_{_format_retry_tasks(retried_tasks)}_"
        blocks.append(
            {
                "type": "section",
                "text": {"type": "mrkdwn", "text": retry_text},
            }
        )

    # Flakiness context: how many failed tasks are known-flaky
    if is_failure and failed_tasks and flaky_map:
        flaky_failed = sum(1 for t in failed_tasks if (t.display_name, t.build_variant) in flaky_map)
        if flaky_failed > 0:
            flaky_run_counts = [
                flaky_map[(t.display_name, t.build_variant)].total_runs
                for t in failed_tasks
                if (t.display_name, t.build_variant) in flaky_map
            ]
            lo, hi = min(flaky_run_counts), max(flaky_run_counts)
            runs_text = f"over {lo} runs" if lo == hi else f"over {lo}–{hi} runs"
            blocks.append(
                {
                    "type": "section",
                    "text": {
                        "type": "mrkdwn",
                        "text": f"_{flaky_failed} of {num_failures} failed task(s) show flaky behavior in the past 7 days ({runs_text})_",
                    },
                }
            )

    # Previous-master context: how many failed tasks also failed on the previous master build
    if is_failure and failed_tasks and prev_failed:
        prev_failed_count = sum(1 for t in failed_tasks if (t.display_name, t.build_variant) in prev_failed)
        if prev_failed_count > 0:
            blocks.append(
                {
                    "type": "section",
                    "text": {
                        "type": "mrkdwn",
                        "text": f"_{prev_failed_count} of {num_failures} failed task(s) also failed on the previous master build (likely not caused by this commit)_",
                    },
                }
            )

    if is_failure and failed_tasks:
        failures_by_variant: dict[str, list[str]] = {}
        for task in failed_tasks:
            if task.build_variant not in failures_by_variant:
                failures_by_variant[task.build_variant] = []
            failures_by_variant[task.build_variant].append(_format_task_name(task, flaky_map, prev_failed))

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


def send_slack_api_message(
    token: str, channel: str, message: dict[str, object], thread_ts: str | None = None
) -> str | None:
    """Post a message with chat.postMessage and return its ts, or None on failure.

    A thread reply needs the ts of the parent message. An incoming webhook does
    not return one, so threaded follow-ups use the Web API.
    """
    payload = {"channel": channel, **message}
    if thread_ts:
        payload["thread_ts"] = thread_ts
    try:
        response = requests.post(
            SLACK_API_URL,
            json=payload,
            headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
            timeout=30,
        )
        response.raise_for_status()
        data = response.json()
    except (requests.RequestException, ValueError) as e:
        print(f"Failed to send Slack message: {e}", file=sys.stderr)
        return None
    if not data.get("ok"):
        print(f"Failed to send Slack message: {data.get('error')}", file=sys.stderr)
        return None
    print("Slack message sent successfully")
    return data.get("ts")


def print_stdout_report(
    version_info: VersionInfo,
    failed_tasks: list[TaskInfo],
    running_tasks: list[TaskInfo],
    flaky_map: dict[tuple[str, str], FlakyTask] | None = None,
    retried_tasks: list[TaskInfo] | None = None,
) -> None:
    """Print a concise report to stdout.

    Args:
        version_info: Version information
        failed_tasks: List of failed tasks
        running_tasks: List of running/pending tasks
        flaky_map: Optional {(task_name, variant): FlakyTask} from Honeycomb
        retried_tasks: Tasks that ran more than once
    """
    flaky_map = flaky_map or {}
    retried_tasks = retried_tasks or []
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

    if retried_tasks:
        print_task_table(retried_tasks, "Restarted Tasks", flaky_map)


def main() -> None:
    start_time = time.time()

    parser = argparse.ArgumentParser(description="Notify about master or backporting-branch build failures")
    parser.add_argument("--dry-run", action="store_true", help="Print Slack JSON payload (for debugging)")
    parser.add_argument("--version-id", help="Evergreen version ID (overrides env var)")
    args = parser.parse_args()

    version_id = args.version_id or os.environ.get("version_id")
    slack_webhook_url = os.environ.get("SLACK_WEBHOOK_URL")
    slack_bot_token = os.environ.get("SLACK_BOT_TOKEN")
    slack_channel_id = os.environ.get("SLACK_CHANNEL_ID")
    notify_on_success = os.environ.get("NOTIFY_ON_SUCCESS", "true").lower() == "true"

    use_slack_dry_run = args.dry_run

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
        failed_tasks, running_tasks, retried_tasks, total_tasks = get_task_summary(api, version_id, NOTIFICATION_TASKS)
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

    # Look up previous master failures (best-effort, non-fatal).
    prev_failed: set[tuple[str, str]] = set()
    if failed_tasks:
        print("Checking previous master build for same failures...", file=sys.stderr)
        try:
            prev_failed = get_previous_version_failures(api, version_info)
            prev_matches = sum(1 for t in failed_tasks if (t.display_name, t.build_variant) in prev_failed)
            print(
                f"  {prev_matches} of {len(failed_tasks)} failed tasks also failed on previous master", file=sys.stderr
            )
        except Exception as e:
            print(f"Warning: previous master lookup failed: {e}", file=sys.stderr)

    if failed_tasks or running_tasks:
        print(f"Found {len(failed_tasks)} failed, {len(running_tasks)} running tasks", file=sys.stderr)
    else:
        print("No failed or running tasks found", file=sys.stderr)
    print(f"Restarted tasks: {len(retried_tasks)} of {total_tasks}", file=sys.stderr)

    # A webhook cannot return the ts that a thread reply needs. Without a bot
    # token, only the count appears in Slack. The task list stays in the task log.
    message = format_slack_message(
        version_info,
        failed_tasks,
        flaky_map,
        prev_failed,
        retried_tasks,
        total_tasks,
    )
    retry_follow_up = None if failed_tasks else format_retry_follow_up(retried_tasks, total_tasks)

    if use_slack_dry_run:
        payload: dict[str, object] = {"message": message}
        if retry_follow_up:
            payload["thread"] = retry_follow_up
        print(json.dumps(payload, indent=2))
    else:
        print_stdout_report(version_info, failed_tasks, running_tasks, flaky_map, retried_tasks)
        should_notify = bool(failed_tasks) or (not running_tasks and notify_on_success)
        if should_notify:
            if slack_bot_token and slack_channel_id:
                parent_ts = send_slack_api_message(slack_bot_token, slack_channel_id, message)
                if parent_ts and retry_follow_up:
                    send_slack_api_message(
                        slack_bot_token,
                        slack_channel_id,
                        retry_follow_up,
                        thread_ts=parent_ts,
                    )
            elif slack_webhook_url:
                send_slack_notification(slack_webhook_url, message)

    elapsed = time.time() - start_time
    print("\n--- Summary ---", file=sys.stderr)
    print(f"Time: {elapsed:.1f}s | API calls: {_api_call_count}", file=sys.stderr)


if __name__ == "__main__":
    main()
