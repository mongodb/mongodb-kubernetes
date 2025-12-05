#!/usr/bin/env python3
"""
Releases all agent and ops-manager images on every merge to master.

Releases:
1. cloud_manager agent (from release.json)
2. For each OM version defined as anchors in .evergreen.yml:
   - The ops-manager image
   - The corresponding agent (from release.json opsManagerMapping)

skip_if_exists handles already-published images.

Usage:
    python release_on_merge.py [--dry-run]
"""
import json
import logging
import re
import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Optional

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger(__name__)

# Track commands for dry-run summary
_dry_run_commands: List[str] = []


def run_command(cmd: List[str], dry_run: bool = False) -> bool:
    """Run a command and return success status."""
    cmd_str = " ".join(cmd)
    logger.info(f"Running: {cmd_str}")
    if dry_run:
        _dry_run_commands.append(cmd_str)
        return True
    try:
        subprocess.run(cmd, check=True)
        return True
    except subprocess.CalledProcessError as e:
        logger.error(f"Command failed with exit code {e.returncode}")
        return False


def print_dry_run_summary():
    """Print summary of all commands that would be run."""
    if not _dry_run_commands:
        return
    print("\n" + "=" * 80)
    print("DRY RUN SUMMARY - Commands that would be executed:")
    for i, cmd in enumerate(_dry_run_commands, 1):
        print(f"{i}. {cmd}")
    print("=" * 80 + "\n")


def load_release_json() -> Dict:
    with open("release.json", "r") as f:
        return json.load(f)


def get_ops_manager_mapping(release_data: Dict) -> Dict:
    return release_data.get("supportedImages", {}).get("mongodb-agent", {}).get("opsManagerMapping", {})


def get_latest_om_versions_from_evergreen_yaml() -> Dict[str, str]:
    """
    Extract OM versions from .evergreen.yml anchors.

    Returns: {"60": "6.0.27", "70": "7.0.19", "80": "8.0.16"}
    """
    versions = {}
    evergreen_path = Path(".evergreen.yml")

    if not evergreen_path.exists():
        logger.error(".evergreen.yml not found")
        return versions

    content = evergreen_path.read_text()

    # Match patterns like: - &ops_manager_60_latest 6.0.27 #
    pattern = r'-\s*&ops_manager_(\d+)_latest\s+(\S+)\s+#'

    for match in re.finditer(pattern, content):
        major = match.group(1)  # "60", "70", "80"
        version = match.group(2)  # "6.0.27", "7.0.19", "8.0.16"
        versions[major] = version
        logger.info(f"Found OM {major}: {version}")

    return versions


def release_agent(agent_version: str, tools_version: str, dry_run: bool = False) -> bool:
    cmd = [
        "python", "scripts/release/pipeline.py",
        "agent",
        "--build-scenario", "release",
        "--version", agent_version,
        "--agent-tools-version", tools_version,
    ]
    return run_command(cmd, dry_run)


def release_ops_manager(om_version: str, dry_run: bool = False) -> bool:
    cmd = [
        "python", "scripts/release/pipeline.py",
        "ops-manager",
        "--build-scenario", "release",
        "--version", om_version,
    ]
    return run_command(cmd, dry_run)


def main():
    import argparse

    parser = argparse.ArgumentParser(description="Release all images on merge")
    parser.add_argument("--dry-run", action="store_true", help="Print commands without executing")
    args = parser.parse_args()

    release_data = load_release_json()
    ops_manager_mapping = get_ops_manager_mapping(release_data)
    latest_om_versions_per_major = get_latest_om_versions_from_evergreen_yaml()

    success = True

    # 1. Release latest cloud_manager agent
    cloud_manager_agent = ops_manager_mapping.get("cloud_manager")
    cloud_manager_tools = ops_manager_mapping.get("cloud_manager_tools")

    if cloud_manager_agent and cloud_manager_tools:
        logger.info(f"=== Releasing cloud_manager agent: {cloud_manager_agent} ===")
        if not release_agent(cloud_manager_agent, cloud_manager_tools, args.dry_run):
            success = False
    else:
        logger.warning("cloud_manager agent not found in release.json")

    # 2. Release each OM version and its agent
    om_mapping = ops_manager_mapping.get("ops_manager", {})

    for major, om_version in latest_om_versions_per_major.items():
        logger.info(f"=== Processing OM {major} ({om_version}) ===")

        # Release ops-manager image
        logger.info(f"Releasing ops-manager {om_version}")
        if not release_ops_manager(om_version, args.dry_run):
            success = False

        # Release agent for this OM version
        agent_info = om_mapping.get(om_version, {})
        agent_version = agent_info.get("agent_version")
        tools_version = agent_info.get("tools_version")

        if agent_version and tools_version:
            logger.info(f"Releasing agent {agent_version} for OM {om_version}")
            if not release_agent(agent_version, tools_version, args.dry_run):
                success = False
        else:
            logger.warning(f"No agent found for OM {om_version} in release.json")

    if args.dry_run:
        print_dry_run_summary()

    return 0 if success else 1


if __name__ == "__main__":
    sys.exit(main())
