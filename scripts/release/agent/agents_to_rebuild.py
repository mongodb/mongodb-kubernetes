#!/usr/bin/env python3
"""
Detects changes to opsManagerMapping in release.json for triggering agent releases.
Relies on git origin/master vs local release.json
"""
import glob
import json
import logging
import os
import re
import subprocess
import sys
from typing import Dict, List, Optional, Tuple

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    handlers=[logging.StreamHandler(sys.stdout)],
)
logger = logging.getLogger(__name__)


def load_current_release_json() -> Optional[Dict]:
    try:
        with open("release.json", "r") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError) as e:
        logger.error(f"Could not load current release.json: {e}")
        return None


def extract_ops_manager_mapping(release_data: Dict) -> Dict:
    if not release_data:
        return {}
    return release_data.get("supportedImages", {}).get("mongodb-agent", {}).get("opsManagerMapping", {})


def get_evergreen_om_version_anchors(evergreen_path: str = ".evergreen.yml") -> Dict[str, str]:
    """Parse .evergreen.yml for &ops_manager_XX_latest anchors -> OM version.

    Context files (e.g. variables/om70, variables/om80) assign CUSTOM_OM_VERSION
    dynamically by grepping .evergreen.yml for these anchors. Parsing the anchors
    here lets us resolve the OM version without executing shell.
    """
    try:
        with open(evergreen_path, "r") as f:
            content = f.read()
    except (FileNotFoundError, OSError) as e:
        logger.debug(f"Could not parse .evergreen.yml anchors: {e}")
        return {}
    return dict(re.findall(r"&(ops_manager_\d+_latest)\s+(\S+)", content))


def get_tools_version_for_agent(agent_version: str) -> str:
    """Get tools version for a given agent version from release.json"""
    release_data = load_current_release_json()
    if not release_data:
        return "100.12.2"  # Default fallback

    ops_manager_mapping = extract_ops_manager_mapping(release_data)
    ops_manager_versions = ops_manager_mapping.get("ops_manager", {})

    # Search through all OM versions to find matching agent version
    for om_version, agent_tools in ops_manager_versions.items():
        if agent_tools.get("agent_version") == agent_version:
            return agent_tools.get("tools_version", "100.12.2")

    # Check cloud_manager tools version as fallback
    return ops_manager_mapping.get("cloud_manager_tools", "100.12.2")


def get_all_agents_for_rebuild() -> List[Tuple[str, str]]:
    """Returns list of (agent_version, tools_version) tuples for all agents in release.json"""
    agents = []

    release_data = load_current_release_json()
    if not release_data:
        logger.error("Could not load release.json")
        return []

    ops_manager_mapping = extract_ops_manager_mapping(release_data)

    # Get all ops_manager agents
    ops_manager_versions = ops_manager_mapping.get("ops_manager", {})
    for om_version, agent_tools in ops_manager_versions.items():
        agent_version = agent_tools.get("agent_version")
        tools_version = agent_tools.get("tools_version")
        if agent_version and tools_version:
            agents.append((agent_version, tools_version))

    # Get cloud_manager agent
    cloud_manager_agent = ops_manager_mapping.get("cloud_manager")
    cloud_manager_tools = ops_manager_mapping.get("cloud_manager_tools")
    if cloud_manager_agent and cloud_manager_tools:
        agents.append((cloud_manager_agent, cloud_manager_tools))

    # Get the main agent version from release.json root
    main_agent_version = release_data.get("agentVersion")
    if main_agent_version:
        tools_version = get_tools_version_for_agent(main_agent_version)
        agents.append((main_agent_version, tools_version))

    return list(set(agents))


def get_currently_used_agents() -> List[Tuple[str, str]]:
    """Returns list of (agent_version, tools_version) tuples for agents currently used in contexts and cloudmanager agent from release.json"""
    logger.info("Getting currently used agents from contexts")
    agents = []

    try:
        release_data = load_current_release_json()
        if not release_data:
            logger.error("Could not load release.json")
            return []

        ops_manager_mapping = extract_ops_manager_mapping(release_data)
        ops_manager_versions = ops_manager_mapping.get("ops_manager", {})
        anchors = get_evergreen_om_version_anchors()

        # Search all context files
        context_pattern = "scripts/dev/contexts/**/*"
        context_files = glob.glob(context_pattern, recursive=True)

        for context_file in context_files:
            if os.path.isfile(context_file):
                try:
                    with open(context_file, "r") as f:
                        content = f.read()

                        # Extract AGENT_VERSION from the context file
                        for line in content.split("\n"):
                            if line.startswith("export AGENT_VERSION="):
                                agent_version = line.split("=")[1].strip()
                                tools_version = get_tools_version_for_agent(agent_version)
                                agents.append((agent_version, tools_version))
                                logger.info(f"Found agent {agent_version} in {context_file}")
                                break

                        # Extract CUSTOM_OM_VERSION and map to agent version.
                        # Handles static values (export CUSTOM_OM_VERSION=7.0.12 or
                        # CUSTOM_OM_VERSION=6.0.27) and dynamic anchor references
                        # (&ops_manager_80_latest resolved via .evergreen.yml) without
                        # executing shell.
                        om_version = None
                        for line in content.split("\n"):
                            stripped = line.lstrip()
                            if stripped.startswith("CUSTOM_OM_VERSION=") or stripped.startswith(
                                "export CUSTOM_OM_VERSION="
                            ):
                                value = line.split("CUSTOM_OM_VERSION=", 1)[1].strip().strip("\"'")
                                if value and not value.startswith("$"):
                                    om_version = value
                                else:
                                    anchor_match = re.search(r"&(ops_manager_\d+_latest)", line)
                                    if anchor_match:
                                        om_version = anchors.get(anchor_match.group(1))
                                break
                        if om_version and om_version in ops_manager_versions:
                            agent_tools = ops_manager_versions[om_version]
                            agent_version = agent_tools.get("agent_version")
                            tools_version = agent_tools.get("tools_version")
                            if agent_version and tools_version:
                                agents.append((agent_version, tools_version))
                                logger.info(f"Found OM version {om_version} -> agent {agent_version} in {context_file}")

                except Exception as e:
                    logger.debug(f"Error reading context file {context_file}: {e}")

        # Also add the cloudmanager agent from release.json
        cloud_manager_agent = ops_manager_mapping.get("cloud_manager")
        cloud_manager_tools = ops_manager_mapping.get("cloud_manager_tools")
        if cloud_manager_agent and cloud_manager_tools:
            agents.append((cloud_manager_agent, cloud_manager_tools))
            logger.info(f"Found cloudmanager agent from release.json: {cloud_manager_agent}")

        # Also add the main agentVersion from release.json
        main_agent_version = release_data.get("agentVersion")
        if main_agent_version:
            tools_version = get_tools_version_for_agent(main_agent_version)
            agents.append((main_agent_version, tools_version))
            logger.info(f"Found main agent version from release.json: {main_agent_version}")

        unique_agents = list(set(agents))
        logger.info(f"Found {len(unique_agents)} currently used agents")
        return unique_agents

    except Exception as e:
        logger.error(f"Error getting currently used agents: {e}")
        return []
