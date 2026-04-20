#!/usr/bin/env python3
"""
Preflight Ops Manager and agent images using the same version set as release_om_and_agents.py.

Order matches release_om_and_agents.main: cloud_manager agent, then each anchor OM (ops-manager + agent).
Skips duplicate agent tags when the same version would be preflighted twice.
"""
import logging
import os
import subprocess
import sys

from scripts.release.release_om_and_agents import (
    get_latest_om_versions_from_evergreen_yaml,
    get_ops_manager_mapping,
    load_release_json,
)

LOGLEVEL = os.environ.get("LOGLEVEL", "INFO").upper()
logging.basicConfig(level=LOGLEVEL)
logger = logging.getLogger(__name__)


def run_preflight(image: str, version: str, submit: str) -> None:
    cmd = [
        "scripts/dev/run_python.sh",
        "scripts/preflight_images.py",
        "--image",
        image,
        "--version",
        version,
        "--submit",
        submit,
    ]
    logger.info("Running: %s", " ".join(cmd))
    subprocess.run(cmd, check=True)


def main() -> int:
    submit = os.environ.get("PREFLIGHT_SUBMIT", "false").lower()
    if submit not in ("true", "false"):
        logger.error("PREFLIGHT_SUBMIT must be 'true' or 'false'")
        return 1

    release_data = load_release_json()
    ops_mapping = get_ops_manager_mapping(release_data)
    latest_om_versions = sorted(get_latest_om_versions_from_evergreen_yaml())
    om_rows = ops_mapping.get("ops_manager", {})

    cloud_manager_agent = ops_mapping.get("cloud_manager")
    if not cloud_manager_agent:
        logger.error("cloud_manager agent missing from release.json opsManagerMapping")
        return 1

    logger.info("Preflight mongodb-agent %s (cloud_manager)", cloud_manager_agent)
    run_preflight("mongodb-agent", cloud_manager_agent, submit)

    for om_version in latest_om_versions:
        if om_version not in om_rows:
            logger.error("No ops_manager mapping for OM version %s in release.json", om_version)
            return 1
        latest_agent_version_per_om = om_rows[om_version].get("agent_version")
        if not latest_agent_version_per_om:
            logger.error("Missing agent_version for OM %s", om_version)
            return 1

        logger.info("Preflight ops-manager %s", om_version)
        run_preflight("ops-manager", om_version, submit)

        logger.info("Preflight mongodb-agent %s (OM %s)", latest_agent_version_per_om, om_version)
        run_preflight("mongodb-agent", latest_agent_version_per_om, submit)

    return 0


if __name__ == "__main__":
    sys.exit(main())
