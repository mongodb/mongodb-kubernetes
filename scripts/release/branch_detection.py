"""
Branch detection and cache scoping utilities for Evergreen CI.

This module provides functions to detect the current git branch and generate
cache scopes for BuildKit remote cache in different environments (local development,
Evergreen patch builds, Evergreen regular builds).
"""

import os
import subprocess
from typing import Optional

from scripts.release.constants import is_running_in_evg, is_evg_patch, get_version_id


def get_current_branch() -> Optional[str]:
    """
    Detect the current git branch for cache scoping.

    In Evergreen CI:
    - For patch builds: tries to detect the original branch (not evg-pr-test-* branches)
    - For master builds: returns the actual branch name
    - Falls back to 'master' if detection fails

    :return: branch name or 'master' as fallback
    """
    if not is_running_in_evg():
        # Local development - use git directly
        try:
            result = subprocess.run(
                ["git", "rev-parse", "--abbrev-ref", "HEAD"],
                capture_output=True,
                text=True,
                check=True
            )
            branch = result.stdout.strip()
            return branch if branch != "HEAD" else "master"
        except (subprocess.CalledProcessError, FileNotFoundError):
            return "master"

    # Running in Evergreen
    if is_evg_patch():
        # For patch builds, try to detect the original branch
        # This logic is based on scripts/evergreen/precommit_bump.sh
        try:
            # Get all remote refs with their commit hashes
            result = subprocess.run(
                ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"],
                capture_output=True,
                text=True,
                check=True
            )

            # Get current commit hash
            current_commit = subprocess.run(
                ["git", "rev-parse", "HEAD"],
                capture_output=True,
                text=True,
                check=True
            ).stdout.strip()

            # Find branches that point to the current commit, excluding evg-pr-test-* branches
            for line in result.stdout.strip().split('\n'):
                if not line:
                    continue
                parts = line.split()
                if len(parts) >= 2:
                    ref_name, commit_hash = parts[0], parts[1]
                    if commit_hash == current_commit and not ref_name.startswith('origin/evg-pr-test-'):
                        # Remove 'origin/' prefix
                        branch = ref_name.replace('origin/', '', 1)
                        return branch

        except (subprocess.CalledProcessError, FileNotFoundError):
            pass
    else:
        # For non-patch builds, try to get the branch from git
        try:
            result = subprocess.run(
                ["git", "rev-parse", "--abbrev-ref", "HEAD"],
                capture_output=True,
                text=True,
                check=True
            )
            branch = result.stdout.strip()
            if branch and branch != "HEAD":
                return branch
        except (subprocess.CalledProcessError, FileNotFoundError):
            pass

    # Fallback to master
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
    sanitized_branch = ''.join(c if c.isalnum() or c in '-_.' else '-' for c in sanitized_branch)

    return sanitized_branch
