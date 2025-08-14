#!/usr/bin/env python3
"""
Detects changes to opsManagerMapping in release.json for triggering agent releases.
Relies on git origin/master vs local release.json
"""
import json
import os
import re
import subprocess
import sys
from typing import Dict, List, Optional, Tuple


def get_content_from_git(commit: str, file_path: str) -> Optional[str]:
    try:
        result = subprocess.run(
            ["git", "show", f"{commit}:{file_path}"], capture_output=True, text=True, check=True, timeout=30
        )
        return result.stdout
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        return None


def load_release_json_from_master() -> Optional[Dict]:
    base_revision = "origin/master"

    content = get_content_from_git(base_revision, "release.json")
    if not content:
        print(f"Could not retrieve release.json from {base_revision}")
        return None

    try:
        return json.loads(content)
    except json.JSONDecodeError as e:
        print(f"Invalid JSON in base release.json: {e}")
        return None


def load_current_release_json() -> Optional[Dict]:
    try:
        with open("release.json", "r") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError) as e:
        print(f"Could not load current release.json: {e}")
        return None


def extract_ops_manager_mapping(release_data: Dict) -> Dict:
    if not release_data:
        return {}
    return release_data.get("supportedImages", {}).get("mongodb-agent", {}).get("opsManagerMapping", {})


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


def get_om_version_from_evergreen(om_series: str) -> Optional[str]:
    """Extract OM version from .evergreen.yml for a given series (60, 70, 80)"""
    if not os.path.exists(".evergreen.yml"):
        return None

    try:
        with open(".evergreen.yml", "r") as f:
            content = f.read()

        # Extract the OM version using the same pattern as the dev contexts
        pattern = rf"^\s*-\s*&ops_manager_{om_series}_latest\s+(\S+)\s+#"
        match = re.search(pattern, content, re.MULTILINE)
        if match:
            return match.group(1)

    except Exception as e:
        pass

    return None


def scan_dev_contexts_for_agents() -> List[Tuple[str, str]]:
    """Scan all dev context files and extract all agents used (both hardcoded and OM-mapped)"""
    agents = set()

    release_data = load_current_release_json()
    if not release_data:
        print("ERROR: Could not load release.json")
        return []

    ops_manager_mapping = extract_ops_manager_mapping(release_data)
    ops_manager_versions = ops_manager_mapping.get("ops_manager", {})

    # Scan all files in scripts/dev/contexts directory
    contexts_dir = "scripts/dev/contexts"
    if not os.path.exists(contexts_dir):
        print(f"ERROR: {contexts_dir} not found")
        return []

    for root, dirs, files in os.walk(contexts_dir):
        for file in files:
            file_path = os.path.join(root, file)

            try:
                with open(file_path, "r") as f:
                    content = f.read()

                # Extract hardcoded AGENT_VERSION
                agent_matches = re.findall(r"export AGENT_VERSION=([^\s]+)", content)
                for agent_version in agent_matches:
                    tools_version = get_tools_version_for_agent(agent_version)
                    agents.add((agent_version, tools_version))
                    print(f"Found hardcoded agent in {file}: {agent_version}, {tools_version}")

                # Extract OM series from grep patterns and resolve to agents
                om_patterns = re.findall(r"ops_manager_(\d+)_latest", content)
                for om_series in om_patterns:
                    om_version = get_om_version_from_evergreen(om_series)
                    if om_version and om_version in ops_manager_versions:
                        agent_tools = ops_manager_versions[om_version]
                        agent_version = agent_tools.get("agent_version")
                        tools_version = agent_tools.get("tools_version")

                        if agent_version and tools_version:
                            agents.add((agent_version, tools_version))
                            print(
                                f"Found OM-mapped agent in {file}: OM {om_version} -> {agent_version}, {tools_version}"
                            )

            except Exception as e:
                # Skip files that can't be read (binary files, etc.)
                continue

    return list(agents)


def get_dev_context_agents() -> List[Tuple[str, str]]:
    """Returns list of (agent_version, tools_version) tuples for agents used in dev contexts"""
    return scan_dev_contexts_for_agents()


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
        print("ERROR: Could not load release.json")
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

    return list(set(agents))  # Remove duplicates


def detect_ops_manager_changes() -> List[Tuple[str, str]]:
    """Returns (has_changes, changed_agents_list)"""
    print("=== Detecting OM Mapping Changes (Local vs Base) ===")

    current_release = load_current_release_json()
    if not current_release:
        print("ERROR: Could not load current local release.json")
        return []

    master_release = load_release_json_from_master()
    if not master_release:
        print("WARNING: Could not load base release.json, assuming changes exist")
        return []

    current_mapping = extract_ops_manager_mapping(current_release)
    base_mapping = extract_ops_manager_mapping(master_release)

    if current_mapping != base_mapping:
        return get_changed_agents(current_mapping, base_mapping)
    else:
        return []
