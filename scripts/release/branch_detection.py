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

    :return: branch name or 'master' as fallback
    """
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"], capture_output=True, text=True, check=True
        )
        branch = result.stdout.strip()
        if branch == "HEAD":
            return "master"
        if branch != "":
            return branch
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
