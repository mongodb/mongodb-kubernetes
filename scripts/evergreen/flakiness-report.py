import json
import os
import sys

import requests

EVERGREEN_API = "https://evergreen.mongodb.com/api"


def print_usage():
    print(
        """Set EVERGREEN_USER and EVERGREEN_API_KEY env. variables
Obtain the version from either Evergreen UI or Github checks
Call
  python flakiness-report.py <patch version number>
  python flakiness-report.py 62cfba5957e85a64e1f801fa"""
    )


def get_variants_with_retried_tasks() -> dict[str, list[dict]]:
    evg_user = os.environ.get("EVERGREEN_USER", "")
    api_key = os.environ.get("API_KEY", "")

    if len(sys.argv) != 2 or evg_user == "" or api_key == "":
        print_usage()
        exit(1)

    version = sys.argv[1]

    headers = {"Api-User": evg_user, "Api-Key": api_key}
    print("Fetching build variants...", file=sys.stderr)
    build_ids = requests.get(url=f"{EVERGREEN_API}/rest/v2/versions/{version}", headers=headers).json()
    build_statuses = [build_status for build_status in build_ids["build_variants_status"]]

    variants_with_retried_tasks: dict[str, list[dict]] = {}
    print(f"Fetching tasks for build variants: ", end="", file=sys.stderr)
    for build_status in build_statuses:
        tasks = requests.get(
            url=f"{EVERGREEN_API}/rest/v2/builds/{build_status['build_id']}/tasks",
            headers=headers,
        ).json()
        retried_tasks = [task for task in tasks if task["execution"] > 1]
        if len(retried_tasks) > 0:
            variants_with_retried_tasks[build_status["build_variant"]] = sorted(
                retried_tasks, key=lambda task: task["execution"], reverse=True
            )
        print(f"{build_status['build_variant']}, ", end="", file=sys.stderr)

    print("", file=sys.stderr)
    return variants_with_retried_tasks


def print_retried_tasks(retried_tasks: dict[str, list[dict]]):
    if len(retried_tasks) == 0:
        print("No retried tasks found")
        return

    print("Number of retries in tasks grouped by build variant:")
    for build_variant, tasks in retried_tasks.items():
        print(f"{build_variant}:")
        for task in tasks:
            print(f"\t{task['display_name']}: {task['execution']}")


def main():
    variants_with_retried_tasks = get_variants_with_retried_tasks()
    print("\n")
    print_retried_tasks(variants_with_retried_tasks)


if __name__ == "__main__":
    main()
