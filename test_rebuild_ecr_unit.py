#!/usr/bin/env python3

"""
Unit test script for ECR to test agent context image rebuild process.

This script creates a mock context image in your ECR account and tests
the rebuild process before running it against the real Quay images in CI.

Usage:
    # Uses default ECR registry or set custom one
    export ECR_REGISTRY=custom-account.dkr.ecr.us-east-1.amazonaws.com  # optional
    python3 test_rebuild_ecr_unit.py
"""

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


def get_ecr_registry() -> str:
    """Get ECR registry from environment variable or use default."""
    default_ecr_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent-ubi"
    ecr_registry = os.getenv("ECR_REGISTRY", default_ecr_registry)
    return ecr_registry


def create_mock_context_image(docker_cmd: str, ecr_registry: str) -> str:
    """Create a mock context image in ECR for testing."""

    # Use a simple base image and create the expected directory structure
    mock_dockerfile_content = """
FROM registry.access.redhat.com/ubi9/ubi-minimal

# Create the directory structure that a real context image would have
RUN mkdir -p /data /opt/scripts

# Create mock agent and tools files (empty for testing)
RUN touch /data/mongodb-agent.tar.gz /data/mongodb-tools.tgz /data/LICENSE

# Create basic versions of the old scripts (without the new ones)
RUN echo '#!/bin/bash' > /opt/scripts/agent-launcher-shim.sh && \
    echo 'echo "Old agent launcher shim"' >> /opt/scripts/agent-launcher-shim.sh && \
    echo '#!/bin/bash' > /opt/scripts/setup-agent-files.sh && \
    echo 'echo "Old setup agent files"' >> /opt/scripts/setup-agent-files.sh && \
    chmod +x /opt/scripts/agent-launcher-shim.sh /opt/scripts/setup-agent-files.sh

# Note: Intentionally NOT including dummy-probe.sh and dummy-readinessprobe.sh
# to simulate an old context image that needs these files added
"""

    mock_image_name = f"{ecr_registry}/test-mongodb-agent-context"

    print(f"Creating mock context image: {mock_image_name}")

    try:
        # Create temporary Dockerfile for mock image
        with open("Dockerfile.mock-context", "w") as f:
            f.write(mock_dockerfile_content)

        # Build the mock context image
        build_cmd = [docker_cmd, "build", "-f", "Dockerfile.mock-context", "-t", mock_image_name, "."]

        print(f"Building mock image: {' '.join(build_cmd)}")
        result = subprocess.run(build_cmd, check=True, capture_output=True, text=True)
        print("Mock image build successful!")

        # Push to ECR
        push_cmd = [docker_cmd, "push", mock_image_name]
        print(f"Pushing to ECR: {' '.join(push_cmd)}")
        result = subprocess.run(push_cmd, check=True, capture_output=True, text=True)
        print("Mock image push successful!")

        # Clean up temporary Dockerfile
        os.remove("Dockerfile.mock-context")

        return mock_image_name

    except subprocess.CalledProcessError as e:
        print(f"Error creating mock context image:")
        print(f"Command: {' '.join(e.cmd)}")
        print(f"Return code: {e.returncode}")
        print(f"Stdout: {e.stdout}")
        print(f"Stderr: {e.stderr}")

        # Clean up temporary Dockerfile
        try:
            os.remove("Dockerfile.mock-context")
        except:
            pass

        return None


def test_rebuild_with_mock_image(docker_cmd: str, mock_image_name: str) -> bool:
    """Test the rebuild process using the mock context image."""

    test_tag = f"{mock_image_name}-rebuilt"

    print(f"\nTesting rebuild with mock image: {mock_image_name}")
    print(f"Test rebuilt tag: {test_tag}")

    try:
        # Build the rebuilt image using our temporary Dockerfile
        build_cmd = [
            docker_cmd,
            "build",
            "-f",
            "docker/mongodb-agent/Dockerfile.rebuild-context",
            "--build-arg",
            f"OLD_CONTEXT_IMAGE={mock_image_name}",
            "-t",
            test_tag,
            ".",
        ]

        print(f"Building rebuilt image: {' '.join(build_cmd)}")
        result = subprocess.run(build_cmd, check=True, capture_output=True, text=True)
        print("Rebuild successful!")

        # Verify the new files are present
        print("\nVerifying new files are present...")
        verify_cmd = [docker_cmd, "run", "--rm", test_tag, "ls", "-la", "/opt/scripts/"]
        print(f"Running: {' '.join(verify_cmd)}")
        result = subprocess.run(verify_cmd, check=True, capture_output=True, text=True)
        print("Files in /opt/scripts/:")
        print(result.stdout)

        # Check for the new files that should have been added
        expected_new_files = ["dummy-probe.sh", "dummy-readinessprobe.sh"]

        expected_updated_files = ["agent-launcher-shim.sh", "setup-agent-files.sh"]

        missing_files = []
        for expected_file in expected_new_files + expected_updated_files:
            if expected_file not in result.stdout:
                missing_files.append(expected_file)

        if missing_files:
            print(f"ERROR: Missing expected files: {missing_files}")
            return False

        print("✓ All expected files are present!")

        # Test that the new dummy scripts work
        print("\nTesting dummy probe scripts...")

        # Test dummy-probe.sh (should exit 0)
        probe_test_cmd = [docker_cmd, "run", "--rm", test_tag, "/opt/scripts/dummy-probe.sh"]
        result = subprocess.run(probe_test_cmd, capture_output=True, text=True)
        if result.returncode != 0:
            print(f"ERROR: dummy-probe.sh failed with exit code {result.returncode}")
            return False
        print("✓ dummy-probe.sh works correctly!")

        # Test dummy-readinessprobe.sh (should exit 1)
        readiness_test_cmd = [docker_cmd, "run", "--rm", test_tag, "/opt/scripts/dummy-readinessprobe.sh"]
        result = subprocess.run(readiness_test_cmd, capture_output=True, text=True)
        if result.returncode != 1:
            print(f"ERROR: dummy-readinessprobe.sh should exit 1 but exited {result.returncode}")
            return False
        print("✓ dummy-readinessprobe.sh works correctly!")

        # Clean up test image
        print(f"\nCleaning up test image: {test_tag}")
        cleanup_cmd = [docker_cmd, "rmi", test_tag]
        subprocess.run(cleanup_cmd, check=True, capture_output=True, text=True)
        print("Test image cleanup successful!")

        return True

    except subprocess.CalledProcessError as e:
        print(f"Error during rebuild test:")
        print(f"Command: {' '.join(e.cmd)}")
        print(f"Return code: {e.returncode}")
        print(f"Stdout: {e.stdout}")
        print(f"Stderr: {e.stderr}")

        # Try to clean up test image if it exists
        try:
            cleanup_cmd = [docker_cmd, "rmi", test_tag]
            subprocess.run(cleanup_cmd, capture_output=True, text=True)
        except:
            pass

        return False


def cleanup_mock_image(docker_cmd: str, mock_image_name: str):
    """Clean up the mock context image from ECR."""
    print(f"\nCleaning up mock image: {mock_image_name}")
    try:
        # Remove local image
        cleanup_cmd = [docker_cmd, "rmi", mock_image_name]
        subprocess.run(cleanup_cmd, capture_output=True, text=True)
        print("Local mock image cleanup successful!")

        print("Note: You may want to manually delete the image from ECR console to avoid charges.")

    except Exception as e:
        print(f"Warning: Could not clean up mock image: {e}")


def main():
    """Main function for ECR unit testing."""
    print("MongoDB Agent Context Image Rebuild - ECR Unit Test")
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

    # Get ECR registry (uses default or environment override)
    ecr_registry = get_ecr_registry()
    print(f"Using ECR registry: {ecr_registry}")

    print("\nThis test will:")
    print("1. Create a mock context image in your ECR account")
    print("2. Test the rebuild process with the mock image")
    print("3. Verify new files are added correctly")
    print("4. Clean up test artifacts")

    # Ask for confirmation
    response = input(f"\nProceed with ECR unit test? (y/N): ")
    if response.lower() != "y":
        print("Aborted.")
        sys.exit(0)

    mock_image_name = None

    try:
        # Step 1: Create mock context image
        print(f"\n" + "=" * 60)
        print("STEP 1: Creating mock context image in ECR")
        print("=" * 60)

        mock_image_name = create_mock_context_image(docker_cmd, ecr_registry)
        if not mock_image_name:
            print("Failed to create mock context image.")
            sys.exit(1)

        # Step 2: Test rebuild process
        print(f"\n" + "=" * 60)
        print("STEP 2: Testing rebuild process")
        print("=" * 60)

        if test_rebuild_with_mock_image(docker_cmd, mock_image_name):
            print(f"\n" + "=" * 60)
            print("✓ ECR UNIT TEST SUCCESSFUL!")
            print("=" * 60)
            print("The rebuild process works correctly with ECR.")
            print("You can now run this in CI against Quay with confidence.")
            print("\nTo run against Quay in CI:")
            print("  export AGENT_REBUILD_REGISTRY=quay.io/mongodb")
            print("  python3 rebuild_agent_context_images.py")
        else:
            print(f"\n" + "=" * 60)
            print("✗ ECR UNIT TEST FAILED!")
            print("=" * 60)
            print("Please fix the issues before running in CI.")
            sys.exit(1)

    finally:
        # Step 3: Cleanup
        if mock_image_name:
            print(f"\n" + "=" * 60)
            print("STEP 3: Cleaning up")
            print("=" * 60)
            cleanup_mock_image(docker_cmd, mock_image_name)


if __name__ == "__main__":
    main()
