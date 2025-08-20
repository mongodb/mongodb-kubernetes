#!/usr/bin/env python3
"""
Detects changes to opsManagerMapping in release.json for triggering agent releases.
Relies on git origin/master vs local release.json
"""
import glob
import json
import logging
import os
import subprocess
import sys
from typing import Dict, List, Optional, Tuple

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    handlers=[logging.StreamHandler(sys.stdout)],
)
logger = logging.getLogger(__name__)


def get_content_from_git(commit: str, file_path: str) -> Optional[str]:
    try:
        result = subprocess.run(
            ["git", "show", f"{commit}:{file_path}"], capture_output=True, text=True, check=True, timeout=30
        )
        return result.stdout
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as e:
        logger.error(f"Failed to get {file_path} from git commit {commit}: {e}")
        return None


def load_release_json_from_master() -> Optional[Dict]:
    base_revision = "origin/master"

    content = get_content_from_git(base_revision, "release.json")
    if not content:
        logger.error(f"Could not retrieve release.json from {base_revision}")
        return None

    try:
        return json.loads(content)
    except json.JSONDecodeError as e:
        logger.error(f"Invalid JSON in base release.json: {e}")
        return None


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


def _is_later_agent_version(version1: str, version2: str) -> bool:
    """
    Compare two agent versions and return True if version1 is later than version2.
    Agent versions are in format like "13.37.0.9590-1" or "108.0.12.8846-1"
    """
    if not version1 or not version2:
        return False

    def split_version(version: str) -> List[int]:
        """Split version string into numeric parts, ignoring suffix after '-'"""
        parts = []
        version_part = version.split("-")[0]  # Remove suffix like "-1"
        for part in version_part.split("."):
            try:
                parts.append(int(part))
            except ValueError:
                # If we can't parse a part as int, skip it
                continue
        return parts

    v1_parts = split_version(version1)
    v2_parts = split_version(version2)

    # Compare each part
    max_len = max(len(v1_parts), len(v2_parts))
    for i in range(max_len):
        v1_part = v1_parts[i] if i < len(v1_parts) else 0
        v2_part = v2_parts[i] if i < len(v2_parts) else 0

        if v1_part != v2_part:
            return v1_part > v2_part

    return False  # Versions are equal


def get_changed_agents(current_mapping: Dict, base_mapping: Dict) -> List[Tuple[str, str]]:
    """Returns list of (agent_version, tools_version) tuples for added/changed agents"""
    added_agents = []

    current_om_mapping = current_mapping.get("ops_manager", {})
    master_om_mapping = base_mapping.get("ops_manager", {})

    for om_version, agent_tools_version in current_om_mapping.items():
        if om_version not in master_om_mapping or master_om_mapping[om_version] != agent_tools_version:
            added_agents.append((agent_tools_version["agent_version"], agent_tools_version["tools_version"]))

    current_cm = current_mapping.get("cloud_manager")
    master_cm = base_mapping.get("cloud_manager")
    current_cm_tools = current_mapping.get("cloud_manager_tools")
    master_cm_tools = base_mapping.get("cloud_manager_tools")

    if current_cm != master_cm or current_cm_tools != master_cm_tools:
        added_agents.append((current_cm, current_cm_tools))

    return list(set(added_agents))


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

                        # Extract CUSTOM_OM_VERSION and map to agent version
                        for line in content.split("\n"):
                            if line.startswith("export CUSTOM_OM_VERSION="):
                                om_version = line.split("=")[1].strip()
                                if om_version in ops_manager_versions:
                                    agent_tools = ops_manager_versions[om_version]
                                    agent_version = agent_tools.get("agent_version")
                                    tools_version = agent_tools.get("tools_version")
                                    if agent_version and tools_version:
                                        agents.append((agent_version, tools_version))
                                        logger.info(
                                            f"Found OM version {om_version} -> agent {agent_version} in {context_file}"
                                        )
                                break

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


def detect_ops_manager_changes() -> List[Tuple[str, str]]:
    """Returns (has_changes, changed_agents_list)"""
    logger.info("=== Detecting OM Mapping Changes (Local vs Base) ===")

    current_release = load_current_release_json()
    if not current_release:
        logger.error("Could not load current local release.json")
        return []

    master_release = load_release_json_from_master()
    if not master_release:
        logger.warning("Could not load base release.json, assuming changes exist")
        return []

    current_mapping = extract_ops_manager_mapping(current_release)
    base_mapping = extract_ops_manager_mapping(master_release)

    if current_mapping != base_mapping:
        return get_changed_agents(current_mapping, base_mapping)
    else:
        return []
