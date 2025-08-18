#!/usr/bin/env python3
"""
Test for agent build mapping functionality in atomic_pipeline.py
"""

import json
import unittest
from unittest.mock import patch

class TestAgentBuildMapping(unittest.TestCase):
    """Test cases for agent build mapping functionality."""

    def setUp(self):
        """Set up test fixtures."""
        # Load the actual build_info_agent.json file
        with open("build_info_agent.json", "r") as f:
            self.agent_build_info = json.load(f)

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
            "mongodb_agent_version_amd64",
            "mongodb_agent_version_arm64",
            "mongodb_agent_version_s390x",
            "mongodb_agent_version_ppc64le",
            "mongodb_tools_version_amd64",
            "mongodb_tools_version_arm64",
            "mongodb_tools_version_s390x",
            "mongodb_tools_version_ppc64le",
        }

        # Generate build args for all platforms
        platforms = ["linux/amd64", "linux/arm64", "linux/s390x", "linux/ppc64le"]
        agent_version = "108.0.7.8810-1"
        tools_version = "100.12.0"

        result = generate_agent_build_args(platforms, agent_version, tools_version)
        generated_build_args = set(result.keys())

        # Verify that we generate exactly the build args the Dockerfile expects
        self.assertEqual(
            generated_build_args,
            expected_dockerfile_args,
            f"Generated build args {generated_build_args} don't match expected {expected_dockerfile_args}",
        )

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
        with open(dockerfile_path, "r") as f:
            dockerfile_content = f.read()

        # Define the expected build args
        expected_args = [
            "mongodb_agent_version_amd64",
            "mongodb_agent_version_arm64",
            "mongodb_agent_version_s390x",
            "mongodb_agent_version_ppc64le",
            "mongodb_tools_version_amd64",
            "mongodb_tools_version_arm64",
            "mongodb_tools_version_s390x",
            "mongodb_tools_version_ppc64le",
        ]

        # Verify each expected arg is declared in the Dockerfile
        for arg_name in expected_args:
            self.assertIn(
                f"ARG {arg_name}", dockerfile_content, f"Dockerfile should contain 'ARG {arg_name}' declaration"
            )

    def test_generate_tools_build_args(self):
        """Test generating tools-only build args."""
        platforms = ["linux/amd64", "linux/arm64"]
        tools_version = "100.12.0"

        result = generate_tools_build_args(platforms, tools_version)

        expected = {
            "mongodb_tools_version_amd64": "mongodb-database-tools-rhel88-x86_64-100.12.0.tgz",
            "mongodb_tools_version_arm64": "mongodb-database-tools-rhel88-aarch64-100.12.0.tgz",
        }

        self.assertEqual(result, expected)

    def test_extract_tools_version_from_release(self):
        """Test extracting tools version from release.json structure."""
        release = {"mongodbToolsBundle": {"ubi": "mongodb-database-tools-rhel88-x86_64-100.12.2.tgz"}}

        result = extract_tools_version_from_release(release)
        self.assertEqual(result, "100.12.2")

    def test_tools_build_args_match_init_dockerfiles(self):
        """Test that tools build args match what init-database and init-appdb Dockerfiles expect."""
        platforms = ["linux/amd64", "linux/arm64", "linux/s390x", "linux/ppc64le"]
        tools_version = "100.12.0"

        result = generate_tools_build_args(platforms, tools_version)

        # Verify all expected tools build args are present (no agent args)
        expected_tools_args = {
            "mongodb_tools_version_amd64",
            "mongodb_tools_version_arm64",
            "mongodb_tools_version_s390x",
            "mongodb_tools_version_ppc64le",
        }

        generated_args = set(result.keys())
        self.assertEqual(generated_args, expected_tools_args)

        # Verify no agent args are included
        for arg_name in result.keys():
            self.assertIn("tools", arg_name)
            self.assertNotIn("agent", arg_name)

    def test_url_construction_correctness(self):
        """Test that URLs are constructed correctly with proper trailing slashes."""
        # Test agent build args URL construction
        platforms = ["linux/amd64"]
        agent_version = "108.0.12.8846-1"
        tools_version = "100.12.2"

        result = generate_agent_build_args(platforms, agent_version, tools_version)

        agent_base_url = (
            "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
        )
        tools_base_url = "https://fastdl.mongodb.org/tools/db"

        agent_filename = result["mongodb_agent_version_amd64"]
        tools_filename = result["mongodb_tools_version_amd64"]

        # Test URL construction (what happens in Dockerfile: ${base_url}/${filename})
        agent_url = f"{agent_base_url}/{agent_filename}"
        tools_url = f"{tools_base_url}/{tools_filename}"

        expected_agent_url = "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-108.0.12.8846-1.linux_x86_64.tar.gz"
        expected_tools_url = "https://fastdl.mongodb.org/tools/db/mongodb-database-tools-rhel88-x86_64-100.12.2.tgz"

        self.assertEqual(agent_url, expected_agent_url)
        self.assertEqual(tools_url, expected_tools_url)

        # Verify no double slashes (common mistake)
        self.assertNotIn("//", agent_url.replace("https://", ""))
        self.assertNotIn("//", tools_url.replace("https://", ""))

        # Verify base URLs do NOT end with slash (to avoid double slashes in Dockerfile)
        self.assertFalse(agent_base_url.endswith("/"))
        self.assertFalse(tools_base_url.endswith("/"))

    def test_agent_version_validation(self):
        """Test that agent version validation works correctly."""
        from scripts.release.agent.validation import validate_tools_version_exists
        from scripts.release.agent.validation import validate_agent_version_exists

        platforms = ["linux/amd64"]

        # Test with a known good agent version (this should exist)
        good_agent_version = "108.0.12.8846-1"
        self.assertTrue(validate_agent_version_exists(good_agent_version, platforms))

        # Test with a known bad agent version (this should not exist)
        bad_agent_version = "12.0.33.7866-1"
        self.assertFalse(validate_agent_version_exists(bad_agent_version, platforms))

        # Test with a known good tools version (this should exist)
        good_tools_version = "100.12.2"
        self.assertTrue(validate_tools_version_exists(good_tools_version, platforms))

        # Test with a known bad tools version (this should not exist)
        bad_tools_version = "999.99.99"
        self.assertFalse(validate_tools_version_exists(bad_tools_version, platforms))


if __name__ == "__main__":
    unittest.main()
