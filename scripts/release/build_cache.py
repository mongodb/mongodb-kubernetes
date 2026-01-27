"""
BuildKit remote cache utilities for Docker image builds.

This module consolidates all caching logic: cache write decisions,
ECR repository management, and BuildKit cache configuration generation.

Cache Strategy:
- All builds read from the master cache
- Only mainline merges (gitter_request) write to master cache
- PRs, manual patches, and other builds are read-only to prevent cache pollution
"""

import json
import os
from pathlib import Path
from typing import Any, Optional

import boto3
from botocore.exceptions import ClientError

from lib.base_logger import logger

# Path to the shared lifecycle policy JSON file
_LIFECYCLE_POLICY_PATH = Path(__file__).parent / "cache_lifecycle_policy.json"


# Requester types that are allowed to write to the master cache
# - gitter_request: mainline commits merged to master
_CACHE_WRITE_REQUESTERS = {"gitter_request"}


def should_write_cache() -> bool:
    """
    Determine if this build should write to the master cache.

    Only mainline merges and merge queue builds write to cache.
    All other builds (PRs, manual patches, etc.) are read-only.

    :return: True if this build should write to cache, False otherwise
    """
    requester = os.environ.get("requester", "")
    should_write = requester in _CACHE_WRITE_REQUESTERS
    logger.debug(f"Cache write decision: requester={requester}, write={should_write}")
    return should_write


def get_cache_lifecycle_policy() -> dict:
    """
    Get the standard lifecycle policy for cache repositories.

    This policy ensures automatic cleanup of cache images:
    - Keep only the latest master cache image
    - Expire branch caches after 14 days of inactivity

    The policy is loaded from a shared JSON file (cache_lifecycle_policy.json)
    to ensure consistency across all modules that need it.

    :return: Lifecycle policy dictionary suitable for ECR put_lifecycle_policy
    """
    with open(_LIFECYCLE_POLICY_PATH) as f:
        return json.load(f)


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


def build_cache_configuration(
    base_registry: str,
) -> tuple[list[Any], Optional[dict[str, str]]]:
    """
    Build cache configuration for BuildKit remote cache.

    All builds read from the master cache.
    Only mainline merges and merge queue builds write to the master cache.

    Uses mode=max to cache all intermediate layers (not just final), oci-mediatypes for
    broad registry compatibility, and image-manifest to store cache as a proper manifest.

    :param base_registry: Base registry URL for cache
    :return: tuple of (cache_from_refs, cache_to_refs) where cache_to_refs may be None
    """
    master_ref = f"{base_registry}:master"

    # All builds read from master cache
    cache_from_refs = [{"type": "registry", "ref": master_ref}]

    # Only mainline merges write to master cache
    if should_write_cache():
        cache_to_refs = {
            "type": "registry",
            "ref": master_ref,
            "mode": "max",
            "oci-mediatypes": "true",
            "image-manifest": "true",
        }
        logger.info("Cache config: read from master, write to master")
    else:
        cache_to_refs = None
        logger.info("Cache config: read from master (read-only)")

    return cache_from_refs, cache_to_refs
