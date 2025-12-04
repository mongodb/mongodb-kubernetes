#!/usr/bin/env python3
"""
Checks which agent versions from release.json are missing from Quay.
This script is used to determine which agents need to be released.

Usage:
    python scripts/release/agent/check_agents_in_quay.py
"""
import json
import subprocess
import sys
from typing import List, Set, Tuple

# Add project root to path for imports
sys.path.insert(0, ".")

from scripts.release.agent.detect_ops_manager_changes import get_all_agents_for_rebuild

QUAY_AGENT_REPO = "quay.io/mongodb/mongodb-agent"


def fetch_all_quay_tags(repository: str = QUAY_AGENT_REPO) -> Set[str]:
    """
    Fetch all tags from Quay registry in a single call using skopeo.

    Args:
        repository: The registry/repository to query (default: quay.io/mongodb/mongodb-agent)

    Returns:
        Set of all tag names in the repository
    """
    try:
        result = subprocess.run(
            ["skopeo", "list-tags", f"docker://{repository}"],
            capture_output=True,
            text=True,
            timeout=60,
        )
        if result.returncode != 0:
            print(f"  ⚠️  skopeo failed: {result.stderr}")
            return set()

        data = json.loads(result.stdout)
        return set(data.get("Tags", []))

    except subprocess.TimeoutExpired:
        print(f"  ⚠️  Timeout fetching tags from {repository}")
        return set()
    except json.JSONDecodeError as e:
        print(f"  ⚠️  Failed to parse skopeo output: {e}")
        return set()
    except FileNotFoundError:
        print("  ⚠️  skopeo not found, falling back to individual checks")
        return set()
    except Exception as e:
        print(f"  ⚠️  Error fetching tags: {e}")
        return set()


def get_agents_missing_from_quay(
    all_agents: List[Tuple[str, str]], quay_tags: Set[str]
) -> Tuple[List[Tuple[str, str]], List[Tuple[str, str]]]:
    """
    Filter agents into those present and missing from Quay.

    Args:
        all_agents: List of (agent_version, tools_version) tuples
        quay_tags: Set of all tags currently in Quay

    Returns:
        Tuple of (missing_agents, existing_agents)
    """
    missing_agents = []
    existing_agents = []

    for agent_version, tools_version in all_agents:
        if agent_version in quay_tags:
            existing_agents.append((agent_version, tools_version))
        else:
            missing_agents.append((agent_version, tools_version))

    return missing_agents, existing_agents


def main():
    print("=" * 60)
    print("Agent Release Check - Quay Registry")
    print("=" * 60)
    print()

    # Step 1: Get all agents from release.json
    print("📋 Getting all agents from release.json...")
    all_agents = get_all_agents_for_rebuild()

    if not all_agents:
        print("  No agents found in release.json")
        return 0

    # Remove duplicates while preserving order
    unique_agents = list(dict.fromkeys(all_agents))
    print(f"  Found {len(unique_agents)} unique agent versions")
    print()

    # Step 2: Fetch all tags from Quay in one call
    print(f"🔍 Fetching all tags from {QUAY_AGENT_REPO}...")
    quay_tags = fetch_all_quay_tags(QUAY_AGENT_REPO)

    if not quay_tags:
        print("  ⚠️  Could not fetch Quay tags - cannot determine missing agents")
        return 2

    print(f"  Found {len(quay_tags)} tags in Quay")
    print()

    # Step 3: Compare locally
    print("🔎 Comparing agents...")
    missing_agents, existing_agents = get_agents_missing_from_quay(unique_agents, quay_tags)

    for agent_version, tools_version in existing_agents:
        print(f"  ✅ {agent_version}")

    for agent_version, tools_version in missing_agents:
        print(f"  ❌ {agent_version} - MISSING")

    print()

    # Step 4: Summary
    print("=" * 60)
    print("SUMMARY")
    print("=" * 60)
    print(f"  Total agents in release.json: {len(unique_agents)}")
    print(f"  Already in Quay:              {len(existing_agents)}")
    print(f"  Missing from Quay:            {len(missing_agents)}")
    print()

    if missing_agents:
        print("🚀 Agents that need to be released:")
        for agent_version, tools_version in missing_agents:
            print(f"  - {agent_version} (tools: {tools_version})")
        print()
        return 1  # Return non-zero to indicate agents need release
    else:
        print("✅ All agents are already in Quay - nothing to release")
        return 0


if __name__ == "__main__":
    sys.exit(main())
