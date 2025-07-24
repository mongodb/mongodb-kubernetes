#!/usr/bin/env python3

"""
Single agent rebuild test script.

This script allows you to test the rebuild process with individual agent versions
for easier testing and debugging. You can specify which agent version to test
or let it pick the latest one automatically.

Usage:
    # Test with latest agent version
    python3 test_single_agent_rebuild.py

    # Test with specific agent version
    python3 test_single_agent_rebuild.py --agent-version 108.0.7.8810-1 --tools-version 100.12.0

    # Use custom registry (defaults to your ECR)
    export AGENT_REBUILD_REGISTRY=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi
    python3 test_single_agent_rebuild.py
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
from typing import Dict, Optional, Tuple


def find_docker_executable() -> str:
    """Find docker executable dynamically to work across different environments."""
    docker_cmd = shutil.which("docker")
    if docker_cmd is None:
        raise Exception("Docker executable not found in PATH")
    return docker_cmd


def load_release_json() -> Dict:
    """Load and parse the release.json file."""
    try:
        with open("release.json", "r") as f:
            return json.load(f)
    except FileNotFoundError:
        print("Error: release.json not found. Run this script from the project root.")
        sys.exit(1)
    except json.JSONDecodeError as e:
        print(f"Error parsing release.json: {e}")
        sys.exit(1)


def get_latest_agent_version(release_data: Dict) -> Optional[Tuple[str, str]]:
    """Get the latest agent version and tools version."""
    mongodb_agent = release_data.get("supportedImages", {}).get("mongodb-agent", {})
    ops_manager_mapping = mongodb_agent.get("opsManagerMapping", {}).get("ops_manager", {})

    if not ops_manager_mapping:
        return None

    # Get the latest OpsManager version (highest version number)
    latest_om_version = max(ops_manager_mapping.keys(), key=lambda x: [int(i) for i in x.split(".")])
    latest_details = ops_manager_mapping[latest_om_version]

    agent_version = latest_details.get("agent_version")
    tools_version = latest_details.get("tools_version")

    if agent_version and tools_version:
        return (agent_version, tools_version)

    return None


def get_all_agent_versions(release_data: Dict) -> list[Tuple[str, str]]:
    """Get all available agent versions for selection."""
    mongodb_agent = release_data.get("supportedImages", {}).get("mongodb-agent", {})
    ops_manager_mapping = mongodb_agent.get("opsManagerMapping", {}).get("ops_manager", {})

    agent_versions = []
    for om_version, details in ops_manager_mapping.items():
        agent_version = details.get("agent_version")
        tools_version = details.get("tools_version")

        if agent_version and tools_version:
            agent_versions.append((agent_version, tools_version))

    # Remove duplicates while preserving order
    seen = set()
    unique_versions = []
    for agent_ver, tools_ver in agent_versions:
        key = (agent_ver, tools_ver)
        if key not in seen:
            seen.add(key)
            unique_versions.append(key)

    return unique_versions


def get_registry_config() -> str:
    """Get registry configuration from environment or default to ECR."""
    return os.getenv("AGENT_REBUILD_REGISTRY", "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi")


def build_context_image_name(agent_version: str, tools_version: str, variant: str = "ubi") -> str:
    """Build the context image name for a given agent and tools version."""
    return f"mongodb-kubernetes-mongodb-agent-context-{agent_version}-{tools_version}-{variant}"


def create_mock_context_image(docker_cmd: str, registry: str, agent_version: str, tools_version: str) -> str:
    """Create a mock context image for testing (simulates an old context image without new scripts)."""

    # Create a mock context image that simulates an old one without the new dummy scripts
    mock_dockerfile_content = f"""
FROM registry.access.redhat.com/ubi9/ubi-minimal

# Create the directory structure that a real context image would have
RUN mkdir -p /data /opt/scripts

# Create mock agent and tools files (empty for testing)
RUN touch /data/mongodb-agent.tar.gz /data/mongodb-tools.tgz /data/LICENSE

# Create basic versions of the old scripts (without the new dummy probe scripts)
RUN echo '#!/bin/bash' > /opt/scripts/agent-launcher-shim.sh && \
    echo 'echo "Old agent launcher shim for {agent_version}"' >> /opt/scripts/agent-launcher-shim.sh && \
    echo '#!/bin/bash' > /opt/scripts/setup-agent-files.sh && \
    echo 'echo "Old setup agent files for {agent_version}"' >> /opt/scripts/setup-agent-files.sh && \
    chmod +x /opt/scripts/agent-launcher-shim.sh /opt/scripts/setup-agent-files.sh

# Add a label to identify this as a mock image
LABEL mock_agent_version="{agent_version}" mock_tools_version="{tools_version}"

# Note: Intentionally NOT including dummy-probe.sh and dummy-readinessprobe.sh
# to simulate an old context image that needs these files added
"""

    context_image_name = build_context_image_name(agent_version, tools_version)
    mock_image_name = f"{registry}/{context_image_name}-mock"

    print(f"Creating mock context image: {mock_image_name}")

    try:
        # Create temporary Dockerfile for mock image
        with open("Dockerfile.mock-single-agent", "w") as f:
            f.write(mock_dockerfile_content)

        # Build the mock context image
        build_cmd = [docker_cmd, "build", "-f", "Dockerfile.mock-single-agent", "-t", mock_image_name, "."]

        print(f"Building mock image: {' '.join(build_cmd)}")
        result = subprocess.run(build_cmd, check=True, capture_output=True, text=True)
        print("Mock image build successful!")

        # Clean up temporary Dockerfile
        os.remove("Dockerfile.mock-single-agent")

        return mock_image_name

    except subprocess.CalledProcessError as e:
        print(f"Error creating mock context image:")
        print(f"Command: {' '.join(e.cmd)}")
        print(f"Return code: {e.returncode}")
        print(f"Stdout: {e.stdout}")
        print(f"Stderr: {e.stderr}")

        # Clean up temporary Dockerfile
        try:
            os.remove("Dockerfile.mock-single-agent")
        except:
            pass

        return None


def test_single_agent_rebuild(docker_cmd: str, registry: str, agent_version: str, tools_version: str) -> bool:
    """Test rebuild process for a single agent version."""

    print(f"\n{'='*60}")
    print(f"Testing Agent: {agent_version} with Tools: {tools_version}")
    print(f"{'='*60}")

    # Step 1: Create mock context image
    print("\nStep 1: Creating mock context image...")
    mock_image_name = create_mock_context_image(docker_cmd, registry, agent_version, tools_version)
    if not mock_image_name:
        return False

    try:
        # Step 2: Test rebuild process
        print("\nStep 2: Testing rebuild process...")
        context_image_name = build_context_image_name(agent_version, tools_version)
        rebuilt_image_name = f"{registry}/{context_image_name}-rebuilt"

        build_cmd = [
            docker_cmd,
            "build",
            "-f",
            "docker/mongodb-agent/Dockerfile.rebuild-context",
            "--build-arg",
            f"OLD_CONTEXT_IMAGE={mock_image_name}",
            "-t",
            rebuilt_image_name,
            ".",
        ]

        print(f"Building rebuilt image: {' '.join(build_cmd)}")
        result = subprocess.run(build_cmd, check=True, capture_output=True, text=True)
        print("Rebuild successful!")

        # Step 3: Verify new files are present
        print("\nStep 3: Verifying new files are present...")
        verify_cmd = [docker_cmd, "run", "--rm", rebuilt_image_name, "ls", "-la", "/opt/scripts/"]
        print(f"Running: {' '.join(verify_cmd)}")
        result = subprocess.run(verify_cmd, check=True, capture_output=True, text=True)
        print("Files in /opt/scripts/:")
        print(result.stdout)

        # Check for expected files
        expected_files = ["dummy-probe.sh", "dummy-readinessprobe.sh", "agent-launcher-shim.sh", "setup-agent-files.sh"]

        missing_files = []
        for expected_file in expected_files:
            if expected_file not in result.stdout:
                missing_files.append(expected_file)

        if missing_files:
            print(f"ERROR: Missing expected files: {missing_files}")
            return False

        print("✓ All expected files are present!")

        # Step 4: Test dummy scripts functionality
        print("\nStep 4: Testing dummy probe scripts...")

        # Test dummy-probe.sh (should exit 0)
        probe_test_cmd = [docker_cmd, "run", "--rm", rebuilt_image_name, "/opt/scripts/dummy-probe.sh"]
        result = subprocess.run(probe_test_cmd, capture_output=True, text=True)
        if result.returncode != 0:
            print(f"ERROR: dummy-probe.sh failed with exit code {result.returncode}")
            return False
        print("✓ dummy-probe.sh works correctly (exits 0)!")

        # Test dummy-readinessprobe.sh (should exit 1)
        readiness_test_cmd = [docker_cmd, "run", "--rm", rebuilt_image_name, "/opt/scripts/dummy-readinessprobe.sh"]
        result = subprocess.run(readiness_test_cmd, capture_output=True, text=True)
        if result.returncode != 1:
            print(f"ERROR: dummy-readinessprobe.sh should exit 1 but exited {result.returncode}")
            return False
        print("✓ dummy-readinessprobe.sh works correctly (exits 1)!")

        print(f"\n✓ SUCCESS: Agent {agent_version} rebuild test passed!")
        return True

    except subprocess.CalledProcessError as e:
        print(f"Error during rebuild test:")
        print(f"Command: {' '.join(e.cmd)}")
        print(f"Return code: {e.returncode}")
        print(f"Stdout: {e.stdout}")
        print(f"Stderr: {e.stderr}")
        return False

    finally:
        # Cleanup
        print(f"\nStep 5: Cleaning up test images...")
        try:
            if mock_image_name:
                cleanup_cmd = [docker_cmd, "rmi", mock_image_name]
                subprocess.run(cleanup_cmd, capture_output=True, text=True)
                print(f"Cleaned up mock image: {mock_image_name}")

            context_image_name = build_context_image_name(agent_version, tools_version)
            rebuilt_image_name = f"{registry}/{context_image_name}-rebuilt"
            cleanup_cmd = [docker_cmd, "rmi", rebuilt_image_name]
            subprocess.run(cleanup_cmd, capture_output=True, text=True)
            print(f"Cleaned up rebuilt image: {rebuilt_image_name}")
        except:
            pass


def main():
    """Main function for single agent testing."""
    parser = argparse.ArgumentParser(description="Test agent context image rebuild with single agent")
    parser.add_argument("--agent-version", help="Specific agent version to test")
    parser.add_argument(
        "--tools-version", help="Specific tools version to test (required if agent-version is specified)"
    )
    parser.add_argument("--list-agents", action="store_true", help="List all available agent versions")
    parser.add_argument("--registry", help="Registry to use (overrides AGENT_REBUILD_REGISTRY env var)")

    args = parser.parse_args()

    print("MongoDB Agent Context Image Rebuild - Single Agent Test")
    print("=" * 60)

    # Check if temporary Dockerfile exists
    dockerfile_path = "docker/mongodb-agent/Dockerfile.rebuild-context"
    try:
        with open(dockerfile_path, "r"):
            pass
    except FileNotFoundError:
        print(f"Error: {dockerfile_path} not found.")
        print("Please create the temporary Dockerfile first.")
        sys.exit(1)

    # Find docker executable
    try:
        docker_cmd = find_docker_executable()
        print(f"Using docker executable: {docker_cmd}")
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)

    # Get registry configuration
    registry = args.registry or get_registry_config()
    print(f"Using registry: {registry}")

    # Load release.json
    release_data = load_release_json()

    # Handle list agents option
    if args.list_agents:
        agent_versions = get_all_agent_versions(release_data)
        print(f"\nAvailable agent versions ({len(agent_versions)} total):")
        for i, (agent_ver, tools_ver) in enumerate(agent_versions, 1):
            print(f"  {i:2d}. Agent: {agent_ver}, Tools: {tools_ver}")
        sys.exit(0)

    # Determine which agent version to test
    if args.agent_version and args.tools_version:
        agent_version = args.agent_version
        tools_version = args.tools_version
        print(f"\nTesting specified agent version:")
        print(f"  Agent: {agent_version}")
        print(f"  Tools: {tools_version}")
    elif args.agent_version and not args.tools_version:
        print("Error: --tools-version is required when --agent-version is specified")
        sys.exit(1)
    else:
        # Use latest agent version
        latest_version = get_latest_agent_version(release_data)
        if not latest_version:
            print("Error: Could not find any agent versions in release.json")
            sys.exit(1)

        agent_version, tools_version = latest_version
        print(f"\nTesting latest agent version:")
        print(f"  Agent: {agent_version}")
        print(f"  Tools: {tools_version}")

    # Ask for confirmation
    response = input(f"\nProceed with single agent rebuild test? (y/N): ")
    if response.lower() != "y":
        print("Aborted.")
        sys.exit(0)

    # Run the test
    if test_single_agent_rebuild(docker_cmd, registry, agent_version, tools_version):
        print(f"\n{'='*60}")
        print("✓ SINGLE AGENT TEST SUCCESSFUL!")
        print("=" * 60)
        print("The rebuild process works correctly for this agent.")
        print("You can now test other agents or run the full rebuild.")
    else:
        print(f"\n{'='*60}")
        print("✗ SINGLE AGENT TEST FAILED!")
        print("=" * 60)
        print("Please fix the issues before testing other agents.")
        sys.exit(1)


if __name__ == "__main__":
    main()
