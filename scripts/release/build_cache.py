"""
BuildKit remote cache utilities for Docker image builds.

This module consolidates all caching logic: branch detection for cache scoping,
ECR repository management, and BuildKit cache configuration generation.
"""

import subprocess
from typing import Any, Optional

import boto3
from botocore.exceptions import ClientError

from lib.base_logger import logger


def get_current_branch() -> Optional[str]:
    """
    Detect the current git branch for cache scoping.

    Evergreen CI creates auto-generated branch names (evg-pr-test-*) when running patch builds,
    which would cause cache misses if used directly. We need the original branch name so that
    repeated builds on the same feature branch can share cached layers.

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

    Branch names become Docker image tags for the cache, so they must be sanitized
    to comply with OCI tag naming rules (lowercase alphanumeric, hyphens, underscores, periods).

    :return: cache scope string suitable for use in image tags
    """
    branch = get_current_branch()

    # Sanitize branch name for use in image tags
    # Replace invalid characters with hyphens and convert to lowercase
    sanitized_branch = branch.lower()
    sanitized_branch = "".join(c if c.isalnum() or c in "-_." else "-" for c in sanitized_branch)

    return sanitized_branch


def ensure_ecr_cache_repository(repository_name: str, region: str = "us-east-1"):
    """
    Ensure an ECR repository exists for caching, creating it if necessary.

    Each image gets its own cache repository (dev/cache/<image-name>) to avoid
    cache pollution between unrelated images and to make cache management easier.

    :param repository_name: Name of the ECR repository to create
    :param region: AWS region for ECR
    """
    ecr_client = boto3.client("ecr", region_name=region)
    try:
        _ = ecr_client.create_repository(repositoryName=repository_name)
        logger.info(f"Successfully created ECR cache repository: {repository_name}")
    except ClientError as e:
        error_code = e.response["Error"]["Code"]
        if error_code == "RepositoryAlreadyExistsException":
            logger.info(f"ECR cache repository already exists: {repository_name}")
        else:
            logger.error(f"Failed to create ECR cache repository {repository_name}: {error_code} - {e}")
            raise


def build_cache_configuration(base_registry: str) -> tuple[list[Any], dict[str, str]]:
    """
    Build cache configuration for branch-scoped BuildKit remote cache.

    Each branch gets its own cache scope so that parallel CI runs on different branches
    don't overwrite each other's cache. Read precedence is branch → master so feature
    branches benefit from master's cache on first build, then accumulate their own.
    We only write to the current branch to prevent feature branches from polluting master.

    Uses mode=max to cache all intermediate layers (not just final), oci-mediatypes for
    broad registry compatibility, and image-manifest to store cache as a proper manifest.

    :param base_registry: Base registry URL for cache
    """
    cache_scope = get_cache_scope()

    # Build cache references with read precedence: branch → master
    cache_from_refs = []

    # Read precedence: branch → master
    branch_ref = f"{base_registry}:{cache_scope}"
    master_ref = f"{base_registry}:master"

    # Add to cache_from in order of precedence
    if cache_scope != "master":
        cache_from_refs.append({"type": "registry", "ref": branch_ref})
        cache_from_refs.append({"type": "registry", "ref": master_ref})
    else:
        cache_from_refs.append({"type": "registry", "ref": master_ref})

    cache_to_refs = {
        "type": "registry",
        "ref": branch_ref,
        "mode": "max",
        "oci-mediatypes": "true",
        "image-manifest": "true",
    }

    return cache_from_refs, cache_to_refs
