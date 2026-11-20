#!/usr/bin/env python3
"""Build an MCK Agent image from an mms-automation branch or PR."""

import argparse
import json
import os
import re
import subprocess
from pathlib import Path
from tempfile import TemporaryDirectory

REPO = "10gen/mms-automation"
REGISTRY = "268558157000.dkr.ecr.us-east-1.amazonaws.com"
ROOT = Path(__file__).resolve().parents[2]


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("ref", help="Agent branch name or https://github.com/10gen/mms-automation/pull/<number>")
    parser.add_argument("--push", action="store_true", help="publish both linux/amd64 and linux/arm64 to dev ECR")
    parser.add_argument("--platform", choices=("linux/amd64", "linux/arm64"), help="platform for a local build")
    args = parser.parse_args()
    if args.push and args.platform:
        parser.error("--platform is for local builds only")

    branch = args.ref
    expected_revision = None
    if pr := re.fullmatch(rf"https://github\.com/{REPO}/pull/(\d+)/?", args.ref):
        head = json.loads(
            output("gh", "pr", "view", pr[1], "--repo", REPO, "--json", "headRefName,headRefOid,isCrossRepository")
        )
        if head["isCrossRepository"]:
            parser.error("PR must use a branch in 10gen/mms-automation")
        branch, expected_revision = head["headRefName"], head["headRefOid"]
    if not re.fullmatch(r"[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}", branch):
        parser.error("branch name must also be a valid image tag")

    image = f"{REGISTRY}/dev/mongodb-agent:{branch}"
    version = json.loads((ROOT / "release.json").read_text())["agentVersion"]
    with TemporaryDirectory(prefix="mck-agent-image-") as directory:
        source = Path(directory) / "source"
        subprocess.run(
            ["git", "clone", "--depth=1", "--single-branch", "--branch", branch, f"git@github.com:{REPO}.git", source],
            check=True,
        )
        revision = output("git", "-C", source, "rev-parse", "HEAD")
        if expected_revision and revision != expected_revision:
            parser.error("PR head and fetched branch differ, retry with the current head")
        # git clone --branch also accepts tags. Require an actual branch.
        if output("git", "-C", source, "branch", "--show-current") != branch:
            parser.error("ref is not an Agent branch")
        print(f"{branch} @ {revision} -> {image}", flush=True)
        token = output("gh", "auth", "token", "--hostname", "github.com")

        metadata = Path(directory) / "metadata.json"
        build = [
            "docker",
            "buildx",
            "build",
            "--file",
            ROOT / "docker/mongodb-agent/Dockerfile.branch",
            "--build-arg",
            f"AGENT_RUNTIME=quay.io/mongodb/mongodb-agent:{version}",
            "--secret",
            "id=github_token,env=GH_TOKEN",
            "--label",
            f"org.opencontainers.image.revision={revision}",
            "--tag",
            image,
            "--metadata-file",
            metadata,
        ]
        if args.push:
            subprocess.run(
                ["docker", "login", "--username", "AWS", "--password-stdin", REGISTRY],
                input=output("aws", "ecr", "get-login-password", "--region", "us-east-1") + "\n",
                text=True,
                check=True,
            )
            build += ["--platform", "linux/amd64,linux/arm64", "--push"]
        else:
            build.append("--load")
            if args.platform:
                build += ["--platform", args.platform]
        subprocess.run([*build, source], check=True, env={**os.environ, "GH_TOKEN": token})
        if args.push:
            digest = json.loads(metadata.read_text())["containerimage.digest"]
            print(f"{image}@{digest}")


if __name__ == "__main__":
    main()
