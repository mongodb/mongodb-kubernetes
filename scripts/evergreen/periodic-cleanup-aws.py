import argparse
import json
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import List

import boto3
from botocore.exceptions import ClientError

# Static list of known dev repositories to clean up
REPOSITORIES_NAMES = [
    "dev/mongodb-agent-ubi",
    "dev/mongodb-agent",
    "dev/mongodb-kubernetes-init-appdb",
    "dev/mongodb-kubernetes-database",
    "dev/mongodb-kubernetes-init-database",
    "dev/mongodb-kubernetes-init-ops-manager",
    "dev/mongodb-enterprise-ops-manager-ubi",
    "dev/mongodb-kubernetes",
]
# Prefix for dynamically discovered cache repositories
CACHE_REPOSITORY_PREFIX = "dev/cache/"
REGISTRY_ID = "268558157000"
REGION = "us-east-1"
DEFAULT_AGE_THRESHOLD_DAYS = 1  # Number of days to consider as the age threshold
BOTO_MAX_PAGE_SIZE = 1000

# Path to the shared lifecycle policy JSON file
_LIFECYCLE_POLICY_PATH = Path(__file__).parent.parent / "release" / "cache_lifecycle_policy.json"

ecr_client = boto3.client("ecr", region_name=REGION)


def describe_all_ecr_images(repository: str) -> List[dict]:
    """Retrieve all ECR images from the repository."""
    images = []

    # Boto3 can only return a maximum of 1000 images per request, we need a paginator to retrieve all images
    # from the repository
    paginator = ecr_client.get_paginator("describe_images")

    page_iterator = paginator.paginate(
        repositoryName=repository,
        registryId=REGISTRY_ID,
        PaginationConfig={"PageSize": BOTO_MAX_PAGE_SIZE},
    )

    for page in page_iterator:
        details = page.get("imageDetails", [])
        images.extend(details)

    return images


def filter_tags_to_delete(images: List[dict]) -> List[dict]:
    """Filter the image list to only delete tags matching the pattern, signatures, or untagged images."""
    filtered_images = []
    untagged_count = 0
    for image_detail in images:
        if "imageTags" in image_detail:
            for tag in image_detail["imageTags"]:
                # The Evergreen patch id we use for building the test images tags uses an Object ID
                # https://www.mongodb.com/docs/v6.2/reference/bson-types/#std-label-objectid
                # The first 4 bytes are based on the timestamp, so it will always have the same prefix for a while (_6 in that case)
                # This is valid until and must be changed before: July 2029
                # 70000000 -> decimal -> 1879048192 => Wednesday, July 18, 2029
                # Note that if the operator ever gets to major version 6, some tags can unintentionally match '_6'
                # It is an easy and relatively reliable way of identifying our test images tags
                if "_6" in tag or ".sig" in tag or contains_timestamped_tag(tag):
                    filtered_images.append(
                        {
                            "imageTag": tag,
                            "imagePushedAt": image_detail["imagePushedAt"],
                            "imageDigest": image_detail["imageDigest"],
                        }
                    )
        else:
            filtered_images.append(
                {
                    "imageTag": "",
                    "imagePushedAt": image_detail["imagePushedAt"],
                    "imageDigest": image_detail["imageDigest"],
                }
            )
            untagged_count += 1
    print(f"found {untagged_count} untagged images")
    return filtered_images


# match 107.0.0.8502-1-b20241125T000000Z-arm64
def contains_timestamped_tag(s: str) -> bool:
    if "b" in s and "T" in s and "Z" in s:
        pattern = r"b\d{8}T\d{6}Z"
        return bool(re.search(pattern, s))
    return False


def get_images_with_dates(repository: str) -> List[dict]:
    """Retrieve the list of patch images, corresponding to the regex, with push dates"""
    ecr_images = describe_all_ecr_images(repository)
    print(f"Found {len(ecr_images)} images in repository {repository}")
    images_matching_tag = filter_tags_to_delete(ecr_images)

    return images_matching_tag


def batch_delete_images(repository: str, images: List[dict]) -> None:
    print(f"Deleting {len(images)} images in repository {repository}")

    # If the image is tagged we only delete the tag, not the sha
    images_to_delete = [
        {"imageTag": image["imageTag"]} if image["imageTag"] else {"imageDigest": image["imageDigest"]}
        for image in images
    ]

    # batch_delete_image only support a maximum of 100 images at a time
    for i in range(0, len(images_to_delete), 100):
        batch = images_to_delete[i : i + 100]
        print(f"Deleting batch {i // 100 + 1} with {len(batch)} images...")
        ecr_client.batch_delete_image(repositoryName=repository, registryId=REGISTRY_ID, imageIds=batch)
    print("Deleted images")


def delete_image(repository: str, image_tag: str) -> None:
    ecr_client.batch_delete_image(
        repositoryName=repository,
        registryId=REGISTRY_ID,
        imageIds=[{"imageTag": image_tag}],
    )
    print(f"Deleted image with tag: {image_tag}")


def delete_images(
    repository: str,
    images_with_dates: List[dict],
    age_threshold: int = DEFAULT_AGE_THRESHOLD_DAYS,
    dry_run: bool = False,
) -> None:
    # Get the current time in UTC
    current_time = datetime.now(timezone.utc)

    # Process the images, deleting those older than the threshold
    delete_count = 0
    age_threshold_timedelta = timedelta(days=age_threshold)
    images_to_delete = []
    for image in images_with_dates:
        tag = image["imageTag"]
        push_date = image["imagePushedAt"]
        image_age = current_time - push_date

        log_message_base = f"Image {tag if tag else 'UNTAGGED'} was pushed at {push_date.isoformat()}"
        delete_message = "should be cleaned up" if dry_run else "deleting..."
        if image_age > age_threshold_timedelta:
            print(f"{log_message_base}, older than {age_threshold} day(s), {delete_message}")
            images_to_delete.append(image)
            delete_count += 1
        else:
            print(f"{log_message_base}, not older than {age_threshold} day(s)")
    if not dry_run:
        batch_delete_images(repository, images_to_delete)
    deleted_message = "need to be cleaned up" if dry_run else "deleted"
    print(f"{delete_count} images {deleted_message}")


def cleanup_repository(
    repository: str,
    age_threshold: int = DEFAULT_AGE_THRESHOLD_DAYS,
    dry_run: bool = False,
):
    print(f"Cleaning up images older than {age_threshold} day(s) from repository {repository}")
    print("Getting list of images...")
    images_with_dates = get_images_with_dates(repository)
    print(f"Images matching the pattern: {len(images_with_dates)}")

    # Sort the images by their push date (oldest first)
    images_with_dates.sort(key=lambda x: x["imagePushedAt"])

    delete_images(repository, images_with_dates, age_threshold, dry_run)
    print(f"Repository {repository} cleaned up")


def discover_cache_repositories() -> List[str]:
    """
    Discover all cache repositories (dev/cache/*) in ECR.

    Cache repositories are created dynamically during builds, so we need to
    discover them rather than maintaining a static list.

    :return: List of cache repository names
    """
    cache_repos = []
    try:
        paginator = ecr_client.get_paginator("describe_repositories")
        page_iterator = paginator.paginate(registryId=REGISTRY_ID)

        for page in page_iterator:
            for repo in page.get("repositories", []):
                repo_name = repo["repositoryName"]
                if repo_name.startswith(CACHE_REPOSITORY_PREFIX):
                    cache_repos.append(repo_name)

        print(f"Discovered {len(cache_repos)} cache repositories")
    except ClientError as e:
        print(f"Warning: Failed to discover cache repositories: {e}")

    return cache_repos


def get_cache_lifecycle_policy() -> dict:
    """
    Get the standard lifecycle policy for cache repositories.

    The policy is loaded from a shared JSON file (scripts/release/cache_lifecycle_policy.json)
    to ensure consistency with the build_cache module.

    :return: Lifecycle policy dictionary suitable for ECR put_lifecycle_policy
    """
    with open(_LIFECYCLE_POLICY_PATH) as f:
        return json.load(f)


def ensure_cache_lifecycle_policies(dry_run: bool = False) -> None:
    """
    Ensure all cache repositories have the correct lifecycle policy.

    This acts as a safety net in case policies weren't applied during build
    (e.g., due to transient errors or repositories created before policy support).

    :param dry_run: If True, only report what would be done
    """
    cache_repos = discover_cache_repositories()
    if not cache_repos:
        print("No cache repositories found")
        return

    lifecycle_policy = get_cache_lifecycle_policy()
    policy_text = json.dumps(lifecycle_policy)

    for repo in cache_repos:
        try:
            # Check if policy already exists and matches
            try:
                existing = ecr_client.get_lifecycle_policy(repositoryName=repo, registryId=REGISTRY_ID)
                existing_policy = existing.get("lifecyclePolicyText", "")
                if existing_policy == policy_text:
                    print(f"  {repo}: lifecycle policy already up-to-date")
                    continue
            except ClientError as e:
                if e.response["Error"]["Code"] != "LifecyclePolicyNotFoundException":
                    raise
                # No policy exists, we'll create one

            if dry_run:
                print(f"  {repo}: would apply lifecycle policy")
            else:
                ecr_client.put_lifecycle_policy(
                    repositoryName=repo,
                    registryId=REGISTRY_ID,
                    lifecyclePolicyText=policy_text,
                )
                print(f"  {repo}: applied lifecycle policy")
        except ClientError as e:
            print(f"  {repo}: failed to apply lifecycle policy: {e}")


def main():
    parser = argparse.ArgumentParser(description="Process and delete old ECR images.")
    parser.add_argument(
        "--age-threshold",
        type=int,
        default=DEFAULT_AGE_THRESHOLD_DAYS,
        help="Age threshold in days for deleting images (default: 1 day)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="If specified, only display what would be deleted without actually deleting.",
    )
    parser.add_argument(
        "--skip-cache-policies",
        action="store_true",
        help="Skip ensuring lifecycle policies on cache repositories.",
    )
    args = parser.parse_args()

    if args.dry_run:
        print("Dry run - not deleting images")

    # Ensure cache repositories have lifecycle policies
    if not args.skip_cache_policies:
        print("\n=== Ensuring cache repository lifecycle policies ===")
        ensure_cache_lifecycle_policies(dry_run=args.dry_run)

    # Clean up dev repositories
    print("\n=== Cleaning up dev repositories ===")
    for repository in REPOSITORIES_NAMES:
        cleanup_repository(repository, age_threshold=args.age_threshold, dry_run=args.dry_run)


if __name__ == "__main__":
    main()
