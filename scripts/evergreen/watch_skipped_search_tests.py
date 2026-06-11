#!/usr/bin/env python3
"""
Watches the search e2e tests that are temporarily skipped at PR time.

Runs on master commit builds only. Three cases:
- Only the known-broken tests failed → do nothing (expected situation)
- Additional unexpected failures → Slack alert listing the new failures
- None of the expected tests failed → Slack reminder to revert the patchable:false skip

Environment variables required:
- EVERGREEN_USER, EVERGREEN_API_KEY: Evergreen API credentials
- SLACK_WEBHOOK_URL: Slack webhook URL
- version_id: The Evergreen version ID to check

Usage:
    python watch_skipped_search_tests.py [--dry-run] [--version-id <id>]
"""

import argparse
import json
import os
import sys

from scripts.evergreen.notify_master_failures import (
    NOTIFICATION_TASKS,
    TaskInfo,
    VersionInfo,
    get_failed_and_running_tasks,
    get_version_info,
    send_slack_notification,
)
from scripts.python.evergreen_api import get_evergreen_api

EXPECTED_FAILING_TASKS = {
    "e2e_search_community_auto_embedding",
    "e2e_search_community_auto_embedding_multi_mongot",
}

EXCLUDE_TASKS = NOTIFICATION_TASKS + ["watch_skipped_search_tests"]


def format_unexpected_failures_message(
    version_info: VersionInfo,
    unexpected_failures: list[TaskInfo],
) -> dict:
    evergreen_url = f"https://spruce.mongodb.com/version/{version_info.version_id}"
    author_mention = f"<@{version_info.author_id}>" if version_info.author_id else version_info.author
    num = len(unexpected_failures)

    failures_by_variant: dict[str, list[str]] = {}
    for task in unexpected_failures:
        failures_by_variant.setdefault(task.build_variant, []).append(task.display_name)

    failure_lines = [f"*{v}*: {', '.join(tasks)}" for v, tasks in sorted(failures_by_variant.items())]

    blocks = [
        {
            "type": "header",
            "text": {"type": "plain_text", "text": "🔴 Unexpected master build failures", "emoji": True},
        },
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
        {"type": "divider"},
        {
            "type": "section",
            "text": {
                "type": "mrkdwn",
                "text": (
                    f"*{num} unexpected {'failure' if num == 1 else 'failures'} "
                    f"(beyond known broken search tests):*\n" + "\n".join(failure_lines)
                ),
            },
        },
        {
            "type": "actions",
            "elements": [
                {
                    "type": "button",
                    "style": "danger",
                    "text": {"type": "plain_text", "text": "View in Evergreen", "emoji": True},
                    "url": evergreen_url,
                }
            ],
        },
    ]
    return {
        "blocks": blocks,
        "text": f"Unexpected master failures: {num} tasks failed beyond known broken search tests",
    }


def format_credentials_restored_message(version_info: VersionInfo) -> dict:
    evergreen_url = f"https://spruce.mongodb.com/version/{version_info.version_id}"
    author_mention = f"<@{version_info.author_id}>" if version_info.author_id else version_info.author

    task_list = "\n".join(f"• `{t}`" for t in sorted(EXPECTED_FAILING_TASKS))
    blocks = [
        {
            "type": "header",
            "text": {"type": "plain_text", "text": "✅ Search test credentials restored", "emoji": True},
        },
        {
            "type": "section",
            "fields": [
                {"type": "mrkdwn", "text": f"*Commit:*\n`{version_info.revision[:8]}`"},
                {"type": "mrkdwn", "text": f"*Author:*\n{author_mention}"},
            ],
        },
        {"type": "divider"},
        {
            "type": "section",
            "text": {
                "type": "mrkdwn",
                "text": (
                    "The following tests passed on master — the credentials issue may be resolved.\n"
                    "*Please revert the `patchable: false` commit that skips them at PR time:*\n" + task_list
                ),
            },
        },
        {
            "type": "actions",
            "elements": [
                {
                    "type": "button",
                    "text": {"type": "plain_text", "text": "View in Evergreen", "emoji": True},
                    "url": evergreen_url,
                }
            ],
        },
    ]
    return {
        "blocks": blocks,
        "text": "Search test credentials may be restored — revert the patchable:false skip commit",
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true", help="Print Slack JSON payload instead of sending")
    parser.add_argument("--version-id", help="Evergreen version ID (overrides env var)")
    args = parser.parse_args()

    version_id = args.version_id or os.environ.get("version_id")
    slack_webhook_url = os.environ.get("SLACK_WEBHOOK_URL")

    if not version_id:
        print("Error: version_id not provided (use --version-id or set env var)", file=sys.stderr)
        sys.exit(1)

    try:
        api = get_evergreen_api()
    except RuntimeError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    version_info = get_version_info(api, version_id)
    all_failures, _ = get_failed_and_running_tasks(api, version_id, EXCLUDE_TASKS)

    expected_failures = [t for t in all_failures if t.display_name in EXPECTED_FAILING_TASKS]
    unexpected_failures = [t for t in all_failures if t.display_name not in EXPECTED_FAILING_TASKS]

    print(f"Expected failures: {[t.display_name for t in expected_failures]}", file=sys.stderr)
    print(f"Unexpected failures: {[t.display_name for t in unexpected_failures]}", file=sys.stderr)

    message = None

    if unexpected_failures:
        print(f"Sending unexpected-failures notification ({len(unexpected_failures)} tasks)", file=sys.stderr)
        message = format_unexpected_failures_message(version_info, unexpected_failures)
    elif not expected_failures:
        print("Expected tests passed — sending credentials-restored reminder", file=sys.stderr)
        message = format_credentials_restored_message(version_info)
    else:
        print("Only expected failures found — no notification needed", file=sys.stderr)

    if message:
        if args.dry_run:
            print(json.dumps(message, indent=2))
        elif slack_webhook_url:
            send_slack_notification(slack_webhook_url, message)
        else:
            print("No SLACK_WEBHOOK_URL set — skipping notification", file=sys.stderr)


if __name__ == "__main__":
    main()
