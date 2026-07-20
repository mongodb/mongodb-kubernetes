#!/usr/bin/env python3
"""
Detects changes to opsManagerMapping in release.json for triggering agent releases.
Relies on git origin/master vs local release.json
"""
import json
import logging
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
    """Returns (agent_version, tools_version) tuples for agents currently used in CI.

    OM variants resolve CUSTOM_OM_VERSION from .evergreen.yml anchors, so the
    agents in use are exactly those mapped to the anchored OM versions, plus
    the default agentVersion and cloud_manager agent from release.json.
    """
    release_data = load_current_release_json()
    if not release_data:
        logger.error("Could not load release.json")
        return []

    ops_manager_mapping = extract_ops_manager_mapping(release_data)
    ops_manager_versions = ops_manager_mapping.get("ops_manager", {})
    agents: List[Tuple[str, str]] = []

    for om_version in get_evergreen_om_version_anchors().values():
        agent_tools = ops_manager_versions.get(om_version)
        if agent_tools:
            agents.append((agent_tools["agent_version"], agent_tools["tools_version"]))

    cloud_manager_agent = ops_manager_mapping.get("cloud_manager")
    cloud_manager_tools = ops_manager_mapping.get("cloud_manager_tools")
    if cloud_manager_agent and cloud_manager_tools:
        agents.append((cloud_manager_agent, cloud_manager_tools))

    main_agent_version = release_data.get("agentVersion")
    if main_agent_version:
        agents.append((main_agent_version, get_tools_version_for_agent(main_agent_version)))

    return list(set(agents))
