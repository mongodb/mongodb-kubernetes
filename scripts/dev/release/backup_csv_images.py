#!/usr/bin/env python3
"""
Script to backup digest-pinned images from a ClusterServiceVersion to quay with _openshift_<mck_version>.

This script parses a ClusterServiceVersion YAML file, extracts all digest-pinned images,
and backs them up to the same quay registry under the with the _openshift_<mck_version> tag.

"""

import argparse
import logging
import os
import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Optional

import yaml

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)

def run_command(cmd: List[str], check: bool = True, dry_run: bool = False) -> Optional[subprocess.CompletedProcess]:
    """Run a shell command and return the result.

    If dry_run is True, only log the command and return None.
    """
    logger.info(f"[DRY RUN] Executing: {' '.join(cmd)}" if dry_run else f"Executing: {' '.join(cmd)}")

    if dry_run:
        return None

    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=check)
        if result.stdout:
            logger.debug(f"Command output: {result.stdout.strip()}")
        return result
    except subprocess.CalledProcessError as e:
        logger.error(f"Command failed: {' '.join(cmd)}")
        logger.error(f"Error: {e.stderr}")
        raise


def get_image_digest(image_url: str) -> str:
    """Get the digest of an image using docker inspect."""
    try:
        # First try to pull the image to ensure we have the latest
        run_command(["docker", "pull", image_url])

        # Get the digest
        result = run_command([
            "docker", "inspect", "--format={{index .RepoDigests 0}}", image_url
        ])

        digest_url = result.stdout.strip()
        if "@sha256:" in digest_url:
            return digest_url.split("@sha256:")[1]
        else:
            logger.warning(f"Could not get digest for {image_url}")
            return ""
    except subprocess.CalledProcessError:
        logger.error(f"Failed to get digest for {image_url}")
        return ""


def parse_csv_file(csv_path: Path) -> Dict:
    """Parse the ClusterServiceVersion YAML file."""
    try:
        with open(csv_path, 'r') as f:
            csv_data = yaml.safe_load(f)
        logger.info(f"Successfully parsed CSV file: {csv_path}")
        return csv_data
    except Exception as e:
        logger.error(f"Failed to parse CSV file {csv_path}: {e}")
        sys.exit(1)


def extract_version_from_csv(csv_data: Dict) -> str:
    """Extract version from CSV metadata."""
    name = csv_data.get('metadata', {}).get('name', '')
    # Common patterns: mongodb-kubernetes.v1.2.0
    version = name.split('.v')[-1]
    logger.debug(f"Extracted version from metadata.name: {version}")
    return version


def extract_images_from_csv(csv_data: Dict) -> Dict[str, str]:
    """Extract all images from the ClusterServiceVersion with their original tags.
        The relatedImages section in spec.relatedImages
    """
    image_url_to_tag = {}  # image_url -> original_tag

    # Extract from relatedImages section
    try:
        related_images = csv_data.get('spec', {}).get('relatedImages', [])
        for related_image in related_images:
            if 'image' in related_image:
                original_tag = extract_tag_from_related_image_name(related_image['name'])
                image_url_to_tag[related_image['image']] = original_tag
                logger.debug(f"Found relatedImages entry: {related_image['image']} -> {original_tag}")
    except Exception as e:
        logger.warning(f"Error extracting relatedImages: {e}")

    logger.info(f"Extracted {len(image_url_to_tag)} total images from CSV")
    return image_url_to_tag


def extract_tag_from_related_image_name(image_name: str) -> str:
    """Convert image name to version string.

    Examples:
        # Agent images
        'agent-image-107-0-10-8627-1-1-1-0' -> '107.0.10.8627-1_1.1.0'
        'agent-image-107-0-10-8627-1' -> '107.0.10.8627-1'

        # Non-agent images
        'mongodb-image' -> ''  # skipped
        'init-appdb-image-repository-1-2-0' -> '1.2.0'
        'ops-manager-image-repository-6-0-25' -> '6.0.25'
    """
    if 'mongodb-image' in image_name:
        return ''

    if 'agent' in image_name.lower():
        parts = image_name.replace('-', '_').split('_')
        version_parts = [p for p in parts if p.isdigit()]

        if not version_parts:
            return ""

        # For agent images, we need at least 4 parts (major, minor, patch, build, magical_one)
        if len(version_parts) >= 5:
            main_version = ".".join(version_parts[:4])  # Join first 3 parts with dots (major.minor.patch.build)
            magical_one = version_parts[4]  # 4th part is the build number
            agent_version = f"{main_version}-{magical_one}"

            # If we have additional parts, they form the operator version
            if len(version_parts) > 5:
                operator_version = ".".join(version_parts[5:])  # Join remaining parts with dots
                return f"{agent_version}_{operator_version}"
            return f"{agent_version}"

        logger.info(f"we had an agent version with an uncommon pattern, skipping it: {image_name}")

    # For non-agent images, take the last segment after the last hyphen
    # and convert remaining hyphens to dots (e.g., '1-2-0' -> '1.2.0')
    if '-' in image_name:
        version_parts = image_name.split('-')[-3:]  # Take last 3 parts for version
        return '.'.join(version_parts)

    return ""


def filter_digest_pinned_images(images: Dict[str, str]) -> Dict[str, str]:
    """Filter images to only include those with digest pins (sha256)."""
    digest_images = {}

    for image_url, original_tag in images.items():
        if "@sha256:" in image_url:
            digest_images[image_url] = original_tag
            logger.debug(f"Found digest-pinned image: {image_url} -> {original_tag}")

    logger.info(f"Found {len(digest_images)} digest-pinned images")
    return digest_images


def parse_image_url(image_url: str) -> tuple[str, str, str]:
    """Parse a digest-pinned image URL into registry, repository, and digest components.

    Args:
        image_url: The digest-pinned image URL (e.g., 'quay.io/mongodb/mongodb-agent-ubi@sha256:abc123')

    Returns:
        A tuple of (registry, repository, digest)
        - registry: The registry part (e.g., 'quay.io')
        - repository: The repository path (e.g., 'mongodb/mongodb-agent-ubi')
        - digest: The image digest (e.g., 'sha256:abc123')
    """
    # Split off the digest
    if "@" not in image_url:
        raise ValueError(f"Expected digest-pinned image URL, got: {image_url}")

    base_url, digest = image_url.split("@", 1)

    # Split registry and repository
    parts = base_url.split("/")
    if len(parts) < 2:
        raise ValueError(f"Invalid image URL format: {image_url}")

    registry = parts[0]
    repository = "/".join(parts[1:])

    return registry, repository, digest


def generate_backup_tag(original_image: str, original_tag: str, mck_version: str) -> str:
    """Generate the backup tag for an image with the following pattern:
        quay.io/mongodb/{repo_name}:{original_tag}_openshift_{version}

    Args:
        original_image: The original image URL (e.g., 'quay.io/mongodb/operator@sha256:abc123')
        original_tag: The original tag to use in the backup tag
        mck_version: The version to append to the backup tag

    Returns:
        The backup tag in the format 'quay.io/mongodb/{repo_name}:{original_tag}_openshift_{version}'
    """
    try:
        # Extract the repository name (last part of the image path)
        # Example: 'quay.io/mongodb/mongodb-agent-ubi' -> 'mongodb-agent-ubi'
        repo_name = original_image.split("@")[0].split("/")[-1]
        return f"quay.io/mongodb/{repo_name}:{original_tag}_openshift_{mck_version}"
    except Exception as e:
        logger.error(f"Failed to generate backup tag for {original_image}: {e}")
        return ""


def image_exists(image_ref: str, dry_run: bool = False) -> bool:
    """Check if an image exists in the remote registry"""
    if dry_run:
        return False  # Always assume image doesn't exist in dry-run mode
        
    try:
        # Use docker manifest inspect to check if the image exists without pulling it
        result = subprocess.run(
            ["docker", "manifest", "inspect", image_ref],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True
        )
        return result.returncode == 0
    except Exception as e:
        logger.debug(f"Error checking if image exists {image_ref}: {e}")
        return False


def backup_image_process(original_image: str, backup_image: str, dry_run: bool = False) -> bool:
    """Backup a single image to Quay, preserving the original digest"""
    try:
        logger.info(f"{'[DRY RUN] ' if dry_run else ''}Backing up {original_image} -> {backup_image}")

        if "@sha256:" not in original_image:
            logger.info(f"Image has no digest, skipping backup")
            return False

        # Check if the backup image already exists
        if image_exists(backup_image, dry_run):
            logger.info(f"Backup image already exists, skipping: {backup_image}")
            return True

        logger.info(f"Pulling {original_image}...")
        run_command(["docker", "pull", original_image], dry_run=dry_run)

        logger.info(f"Tagging as {backup_image}...")
        run_command(["docker", "tag", original_image, backup_image], dry_run=dry_run)

        logger.info(f"Pushing {backup_image}...")
        run_command(["docker", "push", backup_image], dry_run=dry_run)

        logger.info(f"Successfully backed up {original_image}")
        return True

    except subprocess.CalledProcessError as e:
        logger.error(f"Failed to backup {original_image}: {e}")
        return False


def main():
    parser = argparse.ArgumentParser(
        description="Backup digest-pinned images from ClusterServiceVersion to Quay.io"
    )
    parser.add_argument(
        "--skip-login",
        action="store_true",
        help="Skip Quay.io login (use if already authenticated)",
    )
    parser.add_argument(
        "csv_file",
        type=Path,
        help="Path to the ClusterServiceVersion YAML file"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be backed up without actually doing it"
    )

    parser.add_argument(
        "--verbose", "-v",
        action="store_true",
        help="Enable verbose logging"
    )

    parser.add_argument(
        "--limit",
        type=int,
        default=0,
        help="Maximum number of images to back up (0 means no limit)"
    )

    args = parser.parse_args()

    if args.verbose:
        logging.getLogger().setLevel(logging.DEBUG)

    if not args.csv_file.exists():
        logger.error(f"CSV file not found: {args.csv_file}")
        sys.exit(1)

    csv_data = parse_csv_file(args.csv_file)

    mck_version = extract_version_from_csv(csv_data)

    logger.info(f"Using mck_version: {mck_version}")

    all_images = extract_images_from_csv(csv_data)

    digest_pinned_images_to_backup = filter_digest_pinned_images(all_images)
    if not digest_pinned_images_to_backup:
        logger.info("No digest-pinned images found in CSV file")
        return

    backup_plan = []
    for image_url, original_tag in digest_pinned_images_to_backup.items():
        if 'mongodb-enterprise-server' in image_url:
            logger.info(f"Skipping mongodb-enterprise-server image: {image_url}")
            continue

        image_to_backup = generate_backup_tag(image_url, original_tag, mck_version)
        if image_to_backup == "":
            logger.info(f"Skipping image: {image_url}, as it doesn't contain a digest")
            continue
        backup_plan.append((image_url, image_to_backup))

    logger.info(f"Backup plan for {len(backup_plan)} images:")
    for original, backup in backup_plan:
        logger.info(f"  {original} -> {backup}")

    if args.dry_run:
        logger.info("Dry run mode - no images will be backed up")

    # Execute backup
    successful = 0
    failed = 0
    total = len(backup_plan)

    if args.limit > 0:
        logger.info(f"Limiting backup to {args.limit} out of {total} images")
        backup_plan = backup_plan[:args.limit]

    for i, (original_image, image_to_backup) in enumerate(backup_plan, 1):
        logger.info(f"Processing image {i} of {len(backup_plan)}")
        if backup_image_process(original_image, image_to_backup, dry_run=args.dry_run):
            successful += 1
        else:
            failed += 1

    # Summary
    logger.info(f"Backup completed: {successful} successful, {failed} failed")

    if failed > 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
