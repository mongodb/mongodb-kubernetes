#!/usr/bin/env python3

"""SBOM generation orchestrator for MCK.

Generates SBOM Lites for all shipped images and the kubectl-mongodb CLI,
augments them with Kondukto vulnerability data via SilkBomb 2.0,
and uploads the results to S3.

Usage:
    # Daily SBOM generation (cron)
    scripts/dev/run_python.sh generate_sbom.py --mode daily

    # Release SBOM generation (on git tag)
    scripts/dev/run_python.sh generate_sbom.py --mode release

    # Dry run — list images that would be processed
    scripts/dev/run_python.sh generate_sbom.py --mode daily --dry-run
"""

import argparse
import json
from dataclasses import dataclass
from typing import List

from lib.base_logger import logger
from scripts.evergreen.release.agent_matrix import get_supported_version_for_image
from scripts.evergreen.release.sbom import generate_sbom, generate_sbom_for_cli


@dataclass
class ImageTarget:
    """An image+version+platform combination to generate an SBOM for."""

    name: str
    pull_spec: str
    platform: str


def load_json(path: str) -> dict:
    with open(path) as f:
        return json.load(f)


def get_image_targets(release: dict, build_info: dict) -> List[ImageTarget]:
    """Build the full list of image targets from release.json and build_info.json."""
    targets: List[ImageTarget] = []

    # Mapping from build_info image key to release.json version field
    version_map = {
        "operator": release["mongodbOperator"],
        "init-database": release["initDatabaseVersion"],
        "init-ops-manager": release["initOpsManagerVersion"],
        "database": release["databaseImageVersion"],
        "readiness-probe": release["readinessProbeVersion"],
        "upgrade-hook": release["versionUpgradeHookVersion"],
    }

    images = build_info.get("images", {})

    # Standard images (operator, init-database, init-ops-manager, database, readiness-probe, upgrade-hook)
    for image_key, version in version_map.items():
        image_config = images.get(image_key, {})
        release_config = image_config.get("release", {})
        repo = release_config.get("repository", "")
        platforms = release_config.get("platforms", [])
        if not repo or "staging" in repo or "dev" in repo:
            continue
        for platform in platforms:
            targets.append(ImageTarget(name=image_key, pull_spec=f"{repo}:{version}", platform=platform))

    # Agent images — one per supported agent version
    agent_config = images.get("agent", {}).get("release", {})
    agent_repo = agent_config.get("repository", "")
    agent_platforms = agent_config.get("platforms", [])
    if agent_repo:
        agent_versions = get_supported_version_for_image("mongodb-agent")
        for agent_version in agent_versions:
            for platform in agent_platforms:
                targets.append(
                    ImageTarget(name="agent", pull_spec=f"{agent_repo}:{agent_version}", platform=platform)
                )

    # Ops Manager images — one per supported OM version
    om_config = images.get("ops-manager", {}).get("release", {})
    om_repo = om_config.get("repository", "")
    om_platforms = om_config.get("platforms", [])
    if om_repo:
        om_versions = release.get("supportedImages", {}).get("ops-manager", {}).get("versions", [])
        for om_version in om_versions:
            for platform in om_platforms:
                targets.append(
                    ImageTarget(name="ops-manager", pull_spec=f"{om_repo}:{om_version}", platform=platform)
                )

    return targets


def get_cli_platforms() -> List[str]:
    return ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"]


def main():
    parser = argparse.ArgumentParser(description="Generate SBOMs for MCK shipped images and CLI")
    parser.add_argument(
        "--mode",
        choices=["daily", "release"],
        required=True,
        help="daily: generate SBOMs for all shipped images. release: same, run during release pipeline.",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="List what would be generated without actually running.",
    )
    args = parser.parse_args()

    release = load_json("release.json")
    build_info = load_json("build_info.json")
    cli_version = release["mongodbOperator"]

    # Image targets
    image_targets = get_image_targets(release, build_info)
    logger.info(f"Found {len(image_targets)} image targets for SBOM generation")

    if args.dry_run:
        logger.info("=== DRY RUN — no SBOMs will be generated ===")
        for target in image_targets:
            logger.info(f"  Image: {target.pull_spec}  platform={target.platform}")
        for platform in get_cli_platforms():
            logger.info(f"  CLI:   kubectl-mongodb v{cli_version}  platform={platform}")
        return

    # Generate SBOMs for all shipped images
    for target in image_targets:
        logger.info(f"Generating SBOM for {target.pull_spec} ({target.platform})")
        generate_sbom(target.pull_spec, target.platform)

    # Generate SBOMs for CLI binary
    for platform in get_cli_platforms():
        logger.info(f"Generating SBOM for kubectl-mongodb v{cli_version} ({platform})")
        generate_sbom_for_cli(cli_version, platform)

    logger.info("SBOM generation complete")


if __name__ == "__main__":
    main()
