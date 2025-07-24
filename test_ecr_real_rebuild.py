#!/usr/bin/env python3

"""
ECR Real Rebuild Test Script

This script tests the rebuild process using your actual ECR repositories:
- Base image: 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi:<agent_version>
- Target image: 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi-repushed:<agent_version>

Usage:
    # Make sure you're logged in to ECR first
    make aws_login

    # Run the test
    python3 test_ecr_real_rebuild.py

    # Or test with specific agent version
    python3 test_ecr_real_rebuild.py --agent-version 108.0.7.8810-1
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


def test_ecr_real_rebuild(docker_cmd: str, agent_version: str) -> bool:
    """Test rebuild process using real ECR repositories."""

    base_image = f"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi:{agent_version}"
    target_image = f"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi-repushed:{agent_version}"

    print(f"Base image: {base_image}")
    print(f"Target image: {target_image}")

    try:
        # Step 1: Pull the existing base image (force amd64 platform)
        print(f"\n{'='*60}")
        print("STEP 1: Pulling existing base image from ECR")
        print(f"{'='*60}")

        pull_cmd = [docker_cmd, "pull", "--platform", "linux/amd64", base_image]
        print(f"Running: {' '.join(pull_cmd)}")
        result = subprocess.run(pull_cmd, check=True, capture_output=True, text=True)
        print("✓ Base image pull successful!")

        # Step 2: Build the rebuilt image using the temporary Dockerfile (force amd64 platform)
        print(f"\n{'='*60}")
        print("STEP 2: Building rebuilt image with new script files")
        print(f"{'='*60}")

        build_cmd = [
            docker_cmd,
            "build",
            "--platform",
            "linux/amd64",
            "-f",
            "docker/mongodb-agent/Dockerfile.rebuild-context",
            "--build-arg",
            f"OLD_CONTEXT_IMAGE={base_image}",
            "-t",
            target_image,
            ".",
        ]

        print(f"Running: {' '.join(build_cmd)}")
        result = subprocess.run(build_cmd, check=True, capture_output=True, text=True)
        print("✓ Rebuild successful!")

        # Step 3: Verify new files are present in rebuilt image
        print(f"\n{'='*60}")
        print("STEP 3: Verifying new files are present")
        print(f"{'='*60}")

        verify_cmd = [
            docker_cmd,
            "run",
            "--rm",
            "--platform",
            "linux/amd64",
            target_image,
            "ls",
            "-la",
            "/opt/scripts/",
        ]
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
        print(f"\n{'='*60}")
        print("STEP 4: Testing dummy probe scripts functionality")
        print(f"{'='*60}")

        # Test dummy-probe.sh (should exit 0)
        probe_test_cmd = [
            docker_cmd,
            "run",
            "--rm",
            "--platform",
            "linux/amd64",
            target_image,
            "/opt/scripts/dummy-probe.sh",
        ]
        print("Testing dummy-probe.sh (should exit 0)...")
        result = subprocess.run(probe_test_cmd, capture_output=True, text=True)
        if result.returncode != 0:
            print(f"ERROR: dummy-probe.sh failed with exit code {result.returncode}")
            print(f"Stdout: {result.stdout}")
            print(f"Stderr: {result.stderr}")
            return False
        print("✓ dummy-probe.sh works correctly (exits 0)!")

        # Test dummy-readinessprobe.sh (should exit 1)
        readiness_test_cmd = [
            docker_cmd,
            "run",
            "--rm",
            "--platform",
            "linux/amd64",
            target_image,
            "/opt/scripts/dummy-readinessprobe.sh",
        ]
        print("Testing dummy-readinessprobe.sh (should exit 1)...")
        result = subprocess.run(readiness_test_cmd, capture_output=True, text=True)
        if result.returncode != 1:
            print(f"ERROR: dummy-readinessprobe.sh should exit 1 but exited {result.returncode}")
            print(f"Stdout: {result.stdout}")
            print(f"Stderr: {result.stderr}")
            return False
        print("✓ dummy-readinessprobe.sh works correctly (exits 1)!")

        # Step 5: Push the rebuilt image to ECR
        print(f"\n{'='*60}")
        print("STEP 5: Pushing rebuilt image to ECR")
        print(f"{'='*60}")

        push_cmd = [docker_cmd, "push", target_image]
        print(f"Running: {' '.join(push_cmd)}")
        result = subprocess.run(push_cmd, check=True, capture_output=True, text=True)
        print("✓ Push to ECR successful!")

        # Step 6: Verify the pushed image by pulling it fresh
        print(f"\n{'='*60}")
        print("STEP 6: Verifying pushed image by pulling fresh copy")
        print(f"{'='*60}")

        # Remove local copy first
        remove_cmd = [docker_cmd, "rmi", target_image]
        subprocess.run(remove_cmd, capture_output=True, text=True)

        # Pull fresh copy (force amd64 platform)
        pull_fresh_cmd = [docker_cmd, "pull", "--platform", "linux/amd64", target_image]
        print(f"Running: {' '.join(pull_fresh_cmd)}")
        result = subprocess.run(pull_fresh_cmd, check=True, capture_output=True, text=True)
        print("✓ Fresh pull successful!")

        # Verify files are still there
        verify_fresh_cmd = [
            docker_cmd,
            "run",
            "--rm",
            "--platform",
            "linux/amd64",
            target_image,
            "ls",
            "-la",
            "/opt/scripts/",
        ]
        result = subprocess.run(verify_fresh_cmd, check=True, capture_output=True, text=True)

        for expected_file in expected_files:
            if expected_file not in result.stdout:
                print(f"ERROR: {expected_file} missing in fresh pulled image!")
                return False

        print("✓ Fresh pulled image has all expected files!")

        return True

    except subprocess.CalledProcessError as e:
        print(f"Error during ECR rebuild test:")
        print(f"Command: {' '.join(e.cmd)}")
        print(f"Return code: {e.returncode}")
        print(f"Stdout: {e.stdout}")
        print(f"Stderr: {e.stderr}")
        return False


def show_comparison(docker_cmd: str, agent_version: str):
    """Show comparison between base and rebuilt images."""

    base_image = f"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi:{agent_version}"
    target_image = f"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi-repushed:{agent_version}"

    print(f"\n{'='*60}")
    print("COMPARISON: Base vs Rebuilt Image")
    print(f"{'='*60}")

    try:
        print(f"\nBase image files ({agent_version}):")
        base_cmd = [docker_cmd, "run", "--rm", "--platform", "linux/amd64", base_image, "ls", "-la", "/opt/scripts/"]
        result = subprocess.run(base_cmd, capture_output=True, text=True)
        print(result.stdout)

        print(f"\nRebuilt image files ({agent_version}):")
        rebuilt_cmd = [
            docker_cmd,
            "run",
            "--rm",
            "--platform",
            "linux/amd64",
            target_image,
            "ls",
            "-la",
            "/opt/scripts/",
        ]
        result = subprocess.run(rebuilt_cmd, capture_output=True, text=True)
        print(result.stdout)

    except subprocess.CalledProcessError as e:
        print(f"Error during comparison: {e}")


def main():
    """Main function for ECR real rebuild testing."""
    parser = argparse.ArgumentParser(description="Test ECR real rebuild with specific agent version")
    parser.add_argument("--agent-version", help="Specific agent version to test (defaults to latest)")

    args = parser.parse_args()

    print("MongoDB Agent Context Image Rebuild - ECR Real Test")
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

    # Determine agent version to use
    if args.agent_version:
        agent_version = args.agent_version
        print(f"Using specified agent version: {agent_version}")
    else:
        # Load release.json and get latest agent version
        release_data = load_release_json()
        latest_version = get_latest_agent_version(release_data)
        if not latest_version:
            print("Error: Could not find any agent versions in release.json")
            sys.exit(1)

        agent_version, tools_version = latest_version
        print(f"Using latest agent version: {agent_version} (tools: {tools_version})")

    print("\nThis test will:")
    print("1. Pull existing base image from ECR")
    print("2. Build rebuilt image with new script files")
    print("3. Verify new files are present and functional")
    print("4. Push rebuilt image to ECR")
    print("5. Verify pushed image by pulling fresh copy")
    print("6. Show comparison between base and rebuilt images")

    print(f"\nBase image: 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi:{agent_version}")
    print(f"Target image: 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi-repushed:{agent_version}")

    # Ask for confirmation
    response = input(f"\nProceed with ECR real rebuild test? (y/N): ")
    if response.lower() != "y":
        print("Aborted.")
        sys.exit(0)

    # Run the test
    if test_ecr_real_rebuild(docker_cmd, agent_version):
        print(f"\n{'='*60}")
        print("✓ ECR REAL REBUILD TEST SUCCESSFUL!")
        print("=" * 60)
        print("The rebuild process works correctly with real ECR repositories.")
        print("Your rebuilt agent image is now available at:")
        print(f"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi-repushed:{agent_version}")

        # Show comparison
        show_comparison(docker_cmd, agent_version)

        print(f"\n{'='*60}")
        print("NEXT STEPS:")
        print("=" * 60)
        print("1. Verify the rebuilt image works in your applications")
        print("2. If satisfied, you can run this process on all agent versions")
        print("3. For production, use: AGENT_REBUILD_REGISTRY=quay.io/mongodb")

    else:
        print(f"\n{'='*60}")
        print("✗ ECR REAL REBUILD TEST FAILED!")
        print("=" * 60)
        print("Please check the error messages above and fix issues.")
        sys.exit(1)


if __name__ == "__main__":
    main()
