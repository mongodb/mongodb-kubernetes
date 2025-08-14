#!/usr/bin/env python3
"""
Detects changes to opsManagerMapping in release.json for triggering agent releases.
Relies on git origin/master vs local release.json
"""
import json
import os
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


def load_base_release_json() -> Optional[Dict]:
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


def detect_ops_manager_changes() -> List[Tuple[str, str]]:
    """Returns (has_changes, changed_agents_list)"""
    print("=== Detecting OM Mapping Changes (Local vs Base) ===")

    current_release = load_current_release_json()
    if not current_release:
        print("ERROR: Could not load current local release.json")
        return []

    base_release = load_base_release_json()
    if not base_release:
        print("WARNING: Could not load base release.json, assuming changes exist")
        return []

    current_mapping = extract_ops_manager_mapping(current_release)
    base_mapping = extract_ops_manager_mapping(base_release)

    if current_mapping != base_mapping:
        return get_changed_agents(current_mapping, base_mapping)
    else:
        return []


if __name__ == "__main__":
    if not os.path.exists("release.json"):
        print("ERROR: release.json not found. Are you in the project root?")
        sys.exit(2)

    agents = detect_ops_manager_changes()
    if agents:
        print(f"Changed agents: {agents}")
        sys.exit(0)
    else:
        print("Skipping agent release")
        sys.exit(1)
