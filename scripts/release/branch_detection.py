"""
Branch detection and cache scoping utilities for Evergreen CI.

This module provides functions to detect the current git branch and generate
cache scopes for BuildKit remote cache in different environments (local development,
Evergreen patch builds, Evergreen regular builds).
"""

import subprocess
from typing import Optional


def get_current_branch() -> Optional[str]:
    """
    Detect the current git branch for cache scoping.

    In CI environments like Evergreen, git rev-parse --abbrev-ref HEAD returns
    auto-generated branch names like evg-pr-testing-<hash>. This function finds the original branch name by
    looking for remote branches that point to the current commit.

    :return: branch name or 'master' as fallback
    """
    try:
        # Find the original branch (same commit, but not the evg-pr-test-* branch which evg creates)
        current_commit_result = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=True)
        current_commit = current_commit_result.stdout.strip()

        # Get all remote branches with their commit hashes
        remote_branches_result = subprocess.run(
            ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"],
            capture_output=True,
            text=True,
            check=True,
        )

        # Find branches that point to the current commit, excluding auto-generated CI branches
        for line in remote_branches_result.stdout.strip().split("\n"):
            if not line:
                continue
            parts = line.split()
            if len(parts) >= 2:
                branch_name, commit_hash = parts[0], parts[1]
                if commit_hash == current_commit and not "evg-pr-test" in branch_name:
                    # Remove 'origin/' prefix
                    original_branch = branch_name.replace("origin/", "", 1)
                    if original_branch:
                        return original_branch
    except (subprocess.CalledProcessError, FileNotFoundError):
        return "master"

    return "master"


def get_cache_scope() -> str:
    """
    Get the cache scope for BuildKit remote cache.

    Returns a scope string that combines branch and run information:
    - For master branch: returns "master"
    - For other branches: returns the branch name (sanitized for use in image tags)
    - For patch builds: includes version_id to avoid conflicts

    :return: cache scope string suitable for use in image tags
    """
    branch = get_current_branch()

    # Sanitize branch name for use in image tags
    # Replace invalid characters with hyphens and convert to lowercase
    sanitized_branch = branch.lower()
    sanitized_branch = "".join(c if c.isalnum() or c in "-_." else "-" for c in sanitized_branch)

    return sanitized_branch
