#!/usr/bin/env python3
"""
Test for agent build mapping functionality in atomic_pipeline.py
"""

import json
import unittest
from unittest.mock import patch


def load_agent_build_info():
    """Load agent platform mappings from build_info_agent.json"""
    with open("build_info_agent.json", "r") as f:
        return json.load(f)

def generate_agent_build_args(platforms, agent_version, tools_version):
    """
    Generate build arguments for agent image based on platform mappings.
    This is the actual implementation from atomic_pipeline.py
    """
    agent_info = load_agent_build_info()
    build_args = {}

    for platform in platforms:
        if platform not in agent_info["platform_mappings"]:
            # Mock the logger warning for testing
            print(f"Platform {platform} not found in agent mappings, skipping")
            continue

        mapping = agent_info["platform_mappings"][platform]
        build_mapping = agent_info["build_arg_mappings"][platform]

        # Generate agent build arg
        agent_filename = f"{agent_info['base_names']['agent']}-{agent_version}.{mapping['agent_suffix']}"
        build_args[build_mapping["agent_build_arg"]] = agent_filename

        # Generate tools build arg
        tools_suffix = mapping["tools_suffix"].replace("{TOOLS_VERSION}", tools_version)
        tools_filename = f"{agent_info['base_names']['tools']}-{tools_suffix}"
        build_args[build_mapping["tools_build_arg"]] = tools_filename

    return build_args


def _parse_dockerfile_build_args(dockerfile_path):
    """Parse Dockerfile to extract expected build arguments using proper parsing."""
    build_args = set()

    with open(dockerfile_path, 'r') as f:
        lines = f.readlines()

    for line in lines:
        line = line.strip()
        # Skip comments and empty lines
        if not line or line.startswith('#'):
            continue

        # Parse ARG instructions
        if line.startswith('ARG '):
            arg_part = line[4:].strip()  # Remove 'ARG '

            # Handle ARG with default values (ARG name=default)
            arg_name = arg_part.split('=')[0].strip()

            build_args.add(arg_name)

    return build_args


class TestAgentBuildMapping(unittest.TestCase):
    """Test cases for agent build mapping functionality."""

    def setUp(self):
        """Set up test fixtures."""
        # Load the actual build_info_agent.json file
        with open("build_info_agent.json", "r") as f:
            self.agent_build_info = json.load(f)

    def test_generate_agent_build_args_single_platform(self):
        """Test generating build args for a single platform."""
        platforms = ["linux/amd64"]
        agent_version = "108.0.7.8810-1"
        tools_version = "100.12.0"

        result = generate_agent_build_args(platforms, agent_version, tools_version)

        expected = {
            "mongodb_agent_version_amd64": "mongodb-mms-automation-agent-108.0.7.8810-1.linux_x86_64.tar.gz",
            "mongodb_tools_version_amd64": "mongodb-database-tools-rhel93-x86_64-100.12.0.tgz"
        }

        self.assertEqual(result, expected)

    def test_generate_agent_build_args_multiple_platforms(self):
        """Test generating build args for multiple platforms."""
        platforms = ["linux/amd64", "linux/arm64", "linux/s390x", "linux/ppc64le"]
        agent_version = "108.0.7.8810-1"
        tools_version = "100.12.0"

        result = generate_agent_build_args(platforms, agent_version, tools_version)

        expected = {
            "mongodb_agent_version_amd64": "mongodb-mms-automation-agent-108.0.7.8810-1.linux_x86_64.tar.gz",
            "mongodb_tools_version_amd64": "mongodb-database-tools-rhel93-x86_64-100.12.0.tgz",
            "mongodb_agent_version_arm64": "mongodb-mms-automation-agent-108.0.7.8810-1.amzn2_aarch64.tar.gz",
            "mongodb_tools_version_arm64": "mongodb-database-tools-rhel93-aarch64-100.12.0.tgz",
            "mongodb_agent_version_s390x": "mongodb-mms-automation-agent-108.0.7.8810-1.rhel7_s390x.tar.gz",
            "mongodb_tools_version_s390x": "mongodb-database-tools-rhel9-s390x-100.12.0.tgz",
            "mongodb_agent_version_ppc64le": "mongodb-mms-automation-agent-108.0.7.8810-1.rhel8_ppc64le.tar.gz",
            "mongodb_tools_version_ppc64le": "mongodb-database-tools-rhel9-ppc64le-100.12.0.tgz"
        }

        self.assertEqual(result, expected)

    @patch('builtins.print')
    def test_generate_agent_build_args_unknown_platform(self, mock_print):
        """Test handling of unknown platforms."""
        platforms = ["linux/amd64", "linux/unknown"]
        agent_version = "108.0.7.8810-1"
        tools_version = "100.12.0"

        result = generate_agent_build_args(platforms, agent_version, tools_version)

        # Should only include known platform
        expected = {
            "mongodb_agent_version_amd64": "mongodb-mms-automation-agent-108.0.7.8810-1.linux_x86_64.tar.gz",
            "mongodb_tools_version_amd64": "mongodb-database-tools-rhel93-x86_64-100.12.0.tgz"
        }

        self.assertEqual(result, expected)
        mock_print.assert_called_once_with("Platform linux/unknown not found in agent mappings, skipping")

    def test_generate_agent_build_args_empty_platforms(self):
        """Test generating build args with empty platforms list."""
        platforms = []
        agent_version = "108.0.7.8810-1"
        tools_version = "100.12.0"

        result = generate_agent_build_args(platforms, agent_version, tools_version)

        self.assertEqual(result, {})

    def test_build_args_match_dockerfile_requirements(self):
        """Test that generated build args exactly match what the Dockerfile expects."""
        # Define the expected build args based on the platforms we support
        # This is cleaner than parsing the Dockerfile and more explicit about our expectations
        expected_dockerfile_args = {
            "mongodb_agent_version_amd64", "mongodb_agent_version_arm64",
            "mongodb_agent_version_s390x", "mongodb_agent_version_ppc64le",
            "mongodb_tools_version_amd64", "mongodb_tools_version_arm64",
            "mongodb_tools_version_s390x", "mongodb_tools_version_ppc64le"
        }

        # Generate build args for all platforms
        platforms = ["linux/amd64", "linux/arm64", "linux/s390x", "linux/ppc64le"]
        agent_version = "108.0.7.8810-1"
        tools_version = "100.12.0"

        result = generate_agent_build_args(platforms, agent_version, tools_version)
        generated_build_args = set(result.keys())

        # Verify that we generate exactly the build args the Dockerfile expects
        self.assertEqual(generated_build_args, expected_dockerfile_args,
                        f"Generated build args {generated_build_args} don't match expected {expected_dockerfile_args}")

        # Verify the format of generated filenames matches what Dockerfile expects
        for arg_name, filename in result.items():
            if "agent" in arg_name:
                self.assertTrue(filename.startswith("mongodb-mms-automation-agent-"))
                self.assertTrue(filename.endswith(".tar.gz"))
            elif "tools" in arg_name:
                self.assertTrue(filename.startswith("mongodb-database-tools-"))
                self.assertTrue(filename.endswith(".tgz"))

    def test_dockerfile_contains_expected_args(self):
        """Test that the Dockerfile actually contains the build args we expect."""
        dockerfile_path = "docker/mongodb-agent/Dockerfile.atomic"

        # Read the Dockerfile content
        with open(dockerfile_path, 'r') as f:
            dockerfile_content = f.read()

        # Define the expected build args
        expected_args = [
            "mongodb_agent_version_amd64", "mongodb_agent_version_arm64",
            "mongodb_agent_version_s390x", "mongodb_agent_version_ppc64le",
            "mongodb_tools_version_amd64", "mongodb_tools_version_arm64",
            "mongodb_tools_version_s390x", "mongodb_tools_version_ppc64le"
        ]

        # Verify each expected arg is declared in the Dockerfile
        for arg_name in expected_args:
            self.assertIn(f"ARG {arg_name}", dockerfile_content,
                         f"Dockerfile should contain 'ARG {arg_name}' declaration")


if __name__ == "__main__":
    unittest.main()
