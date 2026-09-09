#!/usr/bin/env python3
"""Decides which release pipeline to run and generates evergreen_tasks.json.

Reads the git tag annotation to decide between the new release-publish pipeline
(.evergreen-release-publish.yml) and the deprecated pipeline (.evergreen-release.yml).
Excludes the release_decider variant from the generated output.

Usage:
    scripts/dev/run_python.sh scripts/evergreen/decide_release_process.py
"""

import json
import os
import subprocess
import sys

import yaml


def get_tag_annotation(tag: str) -> str:
    """Return the subject (first line) of a git tag's annotation."""
    result = subprocess.run(
        ["git", "tag", "-l", "--format=%(contents:subject)", tag],
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout.strip()


def main():
    tag = os.environ.get("triggered_by_git_tag")
    if not tag:
        print("ERROR: triggered_by_git_tag is not set", file=sys.stderr)
        sys.exit(1)

    annotation = get_tag_annotation(tag)
    if not annotation:
        print(f"ERROR: no annotation found for tag {tag}", file=sys.stderr)
        sys.exit(1)

    # Decide which pipeline to use.
    if annotation.startswith("[new]"):
        pipeline_file = ".evergreen-release-publish.yml"
        skip_variant = "release_decider"
    else:
        pipeline_file = ".evergreen-release.yml"
        skip_variant = None

    print(f"Decided pipeline: {pipeline_file} for release {tag} (annotation: {annotation})")

    # Handle dry-run.
    if "[dry-run]" in annotation:
        print("Dry run enabled: setting IS_DRYRUN=true")
        with open("evergreen_expansions.yaml", "w") as f:
            f.write("IS_DRYRUN: true\n")

    # Read the pipeline YAML and extract buildvariants.
    with open(pipeline_file) as f:
        config = yaml.safe_load(f)

    variants = config.get("buildvariants", [])

    # Filter out the decider variant for the new pipeline.
    if skip_variant:
        variants = [v for v in variants if v.get("name") != skip_variant]

    if not variants:
        print(f"ERROR: no buildvariants found in {pipeline_file}", file=sys.stderr)
        sys.exit(1)

    # Write generate.tasks JSON. Only emit name + tasks so we add tasks
    # to already-defined variants instead of trying to redefine them.
    #
    # run_conditionally_* wrapper tasks are NOT included — they internally
    # call generate.tasks, which EVG forbids inside another generate.tasks
    # block. Instead we run the condition check here and include the real
    # task directly when it passes.
    def resolve_tasks(v):
        """Return the list of task names for a variant, resolving conditionals inline."""
        out = []
        for t in v.get("tasks", []):
            name = t["name"]
            if name == "run_conditionally_prepare_and_upload_openshift_bundles":
                result = subprocess.run(
                    ["scripts/evergreen/should_prepare_openshift_bundles.sh"],
                    capture_output=True,
                )
                if result.returncode == 0:
                    out.append("prepare_and_upload_openshift_bundles")
            else:
                out.append(name)
        return out

    output = {"buildvariants": [{"name": v["name"], "tasks": resolve_tasks(v)} for v in variants]}
    with open("evergreen_tasks.json", "w") as f:
        json.dump(output, f, indent=2)

    print(f"Generated evergreen_tasks.json with {len(variants)} variants")


if __name__ == "__main__":
    main()
