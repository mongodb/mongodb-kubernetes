import argparse
import re
import sys

import yaml

from scripts.python.evergreen_api import get_task_details


def find_task_variants(evergreen_yml_path: str, task_name: str) -> list[str]:
    with open(evergreen_yml_path, "r") as file:
        evergreen_data = yaml.safe_load(file)

    task_groups = evergreen_data.get("task_groups", [])
    build_variants = evergreen_data.get("buildvariants", [])

    matching_task_groups = [group["name"] for group in task_groups if task_name in group.get("tasks", [])]

    matching_variants: list[str] = []
    for variant in build_variants:
        variant_tasks = variant.get("tasks", [])
        for task_entry in variant_tasks:
            task_name = task_entry.get("name") if isinstance(task_entry, dict) else task_entry
            if task_name in matching_task_groups:
                matching_variants.append(variant["name"])
                break

    return matching_variants


def extract_task_name_from_url(task_url: str) -> str:
    match = re.search(r"/task/([^/]+)/", task_url)
    if not match:
        raise Exception("Could not extract task name from URL")
    return match.group(1)


def find_task_variant_by_url(task_url: str) -> list[str]:
    task_name = extract_task_name_from_url(task_url)
    details = get_task_details(task_name)
    if "build_variant" not in details:
        raise Exception(f'"build_variant" not found in task details: {details}')

    return details["build_variant"]


def main() -> None:
    parser = argparse.ArgumentParser(description="Find Evergreen build variants for a given task.")
    parser.add_argument(
        "--evergreen-file", default=".evergreen.yml", help="Path to evergreen.yml (default: evergreen.yml)"
    )
    parser.add_argument("--task-name", required=False, help="Task name to search for")
    parser.add_argument(
        "--task-url",
        required=False,
        help="Full evergreen url to a task, e.g. https://spruce.mongodb.com/task/mongodb_kubernetes_unit_5e913/logs?execution=0",
    )
    args = parser.parse_args()

    # Ensure exactly one of --task-name or --task-url is provided
    if bool(args.task_name) == bool(args.task_url):
        parser.error("Exactly one of --task-name or --task-url must be provided.")

    try:
        if args.task_name:
            variants = find_task_variants(args.evergreen_file, args.task_name)
        else:
            variants = [find_task_variant_by_url(args.task_url)]
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    if not variants:
        sys.exit(1)

    for variant_name in variants:
        print(variant_name)


if __name__ == "__main__":
    main()
