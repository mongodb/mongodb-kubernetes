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
    python release_om_and_agents.py [--dry-run]
"""
import argparse
import json
import logging
import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Set

import semver
import yaml

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger(__name__)

# Track releases for summary
_releases: List[Dict[str, str]] = []


def run_command(cmd: List[str], dry_run: bool = False) -> bool:
    cmd_str = " ".join(cmd)
    logger.info(f"Running: {cmd_str}")
    if dry_run:
        return True
    try:
        subprocess.run(cmd, check=True)
        return True
    except subprocess.CalledProcessError as e:
        logger.error(f"Command failed with exit code {e.returncode}")
        return False


def track_release(release_type: str, version: str, status: str, context: str = ""):
    _releases.append(
        {
            "type": release_type,
            "version": version,
            "status": status,
            "context": context,
        }
    )


def print_summary(dry_run: bool = False):
    """Print summary of all releases."""
    if not _releases:
        return

    def icon(status: str) -> str:
        if dry_run:
            return "○"
        return "✓" if status == "success" else "✗"

    print("\n" + "=" * 60)
    print("DRY RUN SUMMARY:" if dry_run else "RELEASE SUMMARY:")
    print("=" * 60)

    for release_type, label in [("agent", "Agents"), ("ops-manager", "Ops Manager")]:
        items = [r for r in _releases if r["type"] == release_type]
        if items:
            print(f"\n{label}:")
            for r in items:
                ctx = f" ({r['context']})" if r.get("context") else ""
                print(f"  {icon(r['status'])} {r['version']}{ctx}")

    print(f"\nTotal: {len(_releases)} releases")
    print("=" * 60 + "\n")


def load_release_json() -> Dict:
    with open("release.json", "r") as f:
        return json.load(f)


def get_ops_manager_mapping(release_data: Dict) -> Dict:
    return release_data.get("supportedImages", {}).get("mongodb-agent", {}).get("opsManagerMapping", {})


def get_latest_om_versions_from_evergreen_yaml() -> Set[str]:
    """
    Extract OM versions from .evergreen.yml anchors using YAML parsing and semver.

    Returns: {"6.0.27", "7.0.19", "8.0.16"}
    Raises: RuntimeError if file is missing, malformed, or contains no valid versions
    """
    versions = set()
    evergreen_path = Path(".evergreen.yml")

    if not evergreen_path.exists():
        raise RuntimeError(f".evergreen.yml not found at {evergreen_path.absolute()}")

    try:
        with open(evergreen_path, "r") as f:
            data = yaml.safe_load(f)
    except yaml.YAMLError as e:
        raise RuntimeError(f"Failed to parse .evergreen.yml: {e}") from e
    except Exception as e:
        raise RuntimeError(f"Failed to read .evergreen.yml: {e}") from e

    # Extract version strings from the variables list
    # The YAML structure is: variables: [6.0.27, 7.0.19, 8.0.16, ...]
    variables = data.get("variables", [])

    if not variables:
        raise RuntimeError("No 'variables' list found in .evergreen.yml")

    for var in variables:
        if not isinstance(var, str):
            logger.debug(f"Skipping non-string variable: {type(var).__name__}")
            continue

        try:
            semver.VersionInfo.parse(var)
        except (ValueError, TypeError) as e:
            logger.debug(f"Skipping non-semver variable '{var}': {e}")
            continue

        versions.add(var)

    if not versions:
        raise RuntimeError(
            "No valid OM versions found in .evergreen.yml variables list. "
            "Expected semver strings like '6.0.27', '7.0.19', '8.0.16'"
        )

    return versions


def release_agent(agent_version: str, tools_version: str, context: str, dry_run: bool = False) -> bool:
    """Release an agent image."""
    cmd = [
        "python",
        "scripts/release/pipeline.py",
        "agent",
        "--build-scenario",
        "release",
        "--version",
        agent_version,
        "--agent-tools-version",
        tools_version,
    ]
    success = run_command(cmd, dry_run)
    status = "pending" if dry_run else ("success" if success else "failed")
    track_release("agent", agent_version, status, context)
    return success


def release_ops_manager(om_version: str, dry_run: bool = False) -> bool:
    """Release an ops-manager image."""
    cmd = [
        "python",
        "scripts/release/pipeline.py",
        "ops-manager",
        "--build-scenario",
        "release",
        "--version",
        om_version,
    ]
    success = run_command(cmd, dry_run)
    status = "pending" if dry_run else ("success" if success else "failed")
    track_release("ops-manager", om_version, status)
    return success


def main():
    parser = argparse.ArgumentParser(description="Release all images on merge")
    parser.add_argument("--dry-run", action="store_true", help="Print commands without executing")
    args = parser.parse_args()

    release_data = load_release_json()
    ops_manager_mapping = get_ops_manager_mapping(release_data)
    latest_om_versions = get_latest_om_versions_from_evergreen_yaml()

    release_cm_agent(args, ops_manager_mapping)
    release_om_and_agent(args, latest_om_versions, ops_manager_mapping)

    # Always print summary
    print_summary(args.dry_run)
    return 0


def release_cm_agent(args, ops_manager_mapping: dict):
    # 1. Release latest cloud_manager agent
    cloud_manager_agent = ops_manager_mapping.get("cloud_manager")
    cloud_manager_tools = ops_manager_mapping.get("cloud_manager_tools")

    if not cloud_manager_agent or not cloud_manager_tools:
        raise RuntimeError("cloud_manager agent or tools not found in release.json opsManagerMapping")

    logger.info(f"=== Releasing cloud_manager agent: {cloud_manager_agent} ===")
    if not release_agent(cloud_manager_agent, cloud_manager_tools, "cloud_manager", args.dry_run):
        raise RuntimeError(f"Failed to release cloud_manager agent {cloud_manager_agent}")


def release_om_and_agent(args, latest_om_versions: set[str], ops_manager_mapping: dict):
    # 2. Release each OM version and its agent
    om_mapping = ops_manager_mapping.get("ops_manager", {})

    for om_version in sorted(latest_om_versions):
        logger.info(f"=== Processing OM {om_version} ===")
        agent_info = om_mapping.get(om_version, {})
        agent_version = agent_info.get("agent_version")
        tools_version = agent_info.get("tools_version")

        if not agent_version or not tools_version:
            raise RuntimeError(
                f"Agent mapping incomplete for OM {om_version} in release.json. "
                f"Found: agent_version={agent_version}, tools_version={tools_version}"
            )

        # Release ops-manager image
        logger.info(f"Releasing ops-manager {om_version}")
        if not release_ops_manager(om_version, args.dry_run):
            raise RuntimeError(f"Failed to release ops-manager {om_version}")

        # Release agent for this OM version
        logger.info(f"Releasing agent {agent_version} for OM {om_version}")
        if not release_agent(agent_version, tools_version, f"OM {om_version}", args.dry_run):
            raise RuntimeError(f"Failed to release agent {agent_version} for OM {om_version}")


if __name__ == "__main__":
    sys.exit(main())
