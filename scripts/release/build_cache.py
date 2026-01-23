"""
BuildKit remote cache utilities for Docker image builds.

This module consolidates all caching logic: branch detection for cache scoping,
ECR repository management, and BuildKit cache configuration generation.
"""

import json
import os
import subprocess
from typing import Any, Optional

import boto3
from botocore.exceptions import ClientError

from lib.base_logger import logger


def get_current_branch() -> tuple[str, bool]:
    """
    Detect the current git branch for cache scoping.

    Evergreen CI creates auto-generated branch names (evg-pr-test-*) when running patch builds,
    which would cause cache misses if used directly. We need the original branch name so that
    repeated builds on the same feature branch can share cached layers.

    For fork PRs, we rely on the github_pr_head_branch environment variable set by Evergreen,
    since fork branches won't appear under refs/remotes/origin.

    :return: tuple of (branch_name, found)
             found=False when we fall back to master (e.g., manual patches)
    """
    # First, check for Evergreen's github_pr_head_branch env var (handles fork PRs)
    pr_head_branch = os.environ.get("github_pr_head_branch")
    if pr_head_branch:
        logger.debug(f"Using github_pr_head_branch env var: {pr_head_branch}")
        return pr_head_branch, True

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
                        return original_branch, True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return "master", False

    return "master", False


def get_cache_scope() -> tuple[str, bool]:
    """
    Get the cache scope for BuildKit remote cache.

    Branch names become Docker image tags for the cache, so they must be sanitized
    to comply with OCI tag naming rules (lowercase alphanumeric, hyphens, underscores, periods).

    :return: tuple of (branch_name_to_use, should_write_cache)
             should_write_cache=False when branch detection fell back to master
    """
    branch, found = get_current_branch()

    # Sanitize branch name for use in image tags
    # Replace invalid characters with hyphens and convert to lowercase
    sanitized_branch = branch.lower()
    sanitized_branch = "".join(c if c.isalnum() or c in "-_." else "-" for c in sanitized_branch)

    return sanitized_branch, found


def get_cache_lifecycle_policy() -> dict:
    """
    Get the standard lifecycle policy for cache repositories.

    This policy ensures automatic cleanup of cache images:
    - Keep only the latest master cache image
    - Expire branch caches after 14 days of inactivity

    :return: Lifecycle policy dictionary suitable for ECR put_lifecycle_policy
    """
    return {
        "rules": [
            {
                "rulePriority": 1,
                "description": "Keep only latest master cache",
                "selection": {
                    "tagStatus": "tagged",
                    "tagPatternList": ["master"],
                    "countType": "imageCountMoreThan",
                    "countNumber": 1,
                },
                "action": {"type": "expire"},
            },
            {
                "rulePriority": 2,
                "description": "Expire branch caches after 14 days",
                "selection": {
                    "tagStatus": "tagged",
                    "tagPatternList": ["*"],
                    "countType": "sinceImagePushed",
                    "countUnit": "days",
                    "countNumber": 14,
                },
                "action": {"type": "expire"},
            },
        ]
    }


def apply_cache_lifecycle_policy(ecr_client, repository_name: str) -> bool:
    """
    Apply the standard cache lifecycle policy to an ECR repository.

    This is idempotent - applying the same policy multiple times is safe.
    Failures are logged but don't raise exceptions to avoid breaking builds.

    """
    try:
        lifecycle_policy = get_cache_lifecycle_policy()
        ecr_client.put_lifecycle_policy(
            repositoryName=repository_name,
            lifecyclePolicyText=json.dumps(lifecycle_policy),
        )
        logger.info(f"Applied lifecycle policy to {repository_name}")
        return True
    except ClientError as e:
        # Log warning but don't fail the build - lifecycle policy is nice-to-have
        logger.warning(f"Failed to apply lifecycle policy to {repository_name}: {e}")
        return False


def ensure_ecr_cache_repository(repository_name: str, region: str = "us-east-1"):
    """
    Ensure an ECR repository exists for caching, creating it if necessary.

    Each image gets its own cache repository (dev/cache/<image-name>) to avoid
    cache pollution between unrelated images and to make cache management easier.

    Also ensures the lifecycle policy is applied (for both new and existing repos).

    :param repository_name: Name of the ECR repository to create
    :param region: AWS region for ECR
    """
    ecr_client = boto3.client("ecr", region_name=region)
    try:
        ecr_client.create_repository(repositoryName=repository_name)
        logger.info(f"Successfully created ECR cache repository: {repository_name}")
    except ClientError as e:
        error_code = e.response["Error"]["Code"]
        if error_code == "RepositoryAlreadyExistsException":
            logger.debug(f"ECR cache repository already exists: {repository_name}")
        else:
            logger.error(f"Failed to create ECR cache repository {repository_name}: {error_code} - {e}")
            raise

    # Apply lifecycle policy for automatic cache cleanup (for both new and existing repos)
    # This is idempotent and ensures policy is always up-to-date
    apply_cache_lifecycle_policy(ecr_client, repository_name)


def build_cache_configuration(base_registry: str) -> tuple[list[Any], Optional[dict[str, str]]]:
    """
    Build cache configuration for branch-scoped BuildKit remote cache.

    Each branch gets its own cache scope so that parallel CI runs on different branches
    don't overwrite each other's cache. Read precedence is branch → master so feature
    branches benefit from master's cache on first build, then accumulate their own.
    We only write to the current branch to prevent feature branches from polluting master.

    Uses mode=max to cache all intermediate layers (not just final), oci-mediatypes for
    broad registry compatibility, and image-manifest to store cache as a proper manifest.

    Cache writes are disabled when branch detection falls back to master (e.g., manual patches)
    to prevent polluting the master cache with unrelated builds.

    :param base_registry: Base registry URL for cache
    :return: tuple of (cache_from_refs, cache_to_refs) where cache_to_refs may be None
    """
    cache_scope, should_write_cache = get_cache_scope()

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

    # Only write to cache if branch was positively detected
    # This prevents manual patches from polluting the master cache
    if should_write_cache:
        cache_to_refs = {
            "type": "registry",
            "ref": branch_ref,
            "mode": "max",
            "oci-mediatypes": "true",
            "image-manifest": "true",
        }
    else:
        logger.info(f"Cache writes disabled: branch detection fell back to '{cache_scope}'")
        cache_to_refs = None

    return cache_from_refs, cache_to_refs
