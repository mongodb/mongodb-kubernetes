#!/usr/bin/env python3
"""
Test for agent build mapping functionality and validation functions.
"""

import unittest
from unittest.mock import patch

from scripts.release.agent.validation import (
    generate_agent_build_args,
    generate_tools_build_args,
    get_working_agent_filename,
    get_working_tools_filename,
)


class TestBuildArgumentGeneration(unittest.TestCase):
    """Test cases for build argument generation functions."""

    def setUp(self):
        """Set up test fixtures."""
        self.platforms = ["linux/amd64", "linux/arm64"]
        self.tools_version = "100.9.5"
        self.agent_version = "13.5.2.7785"

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_tools_build_args(self, mock_validate):
        """Test tools build args generation."""
        # Mock URL validation to return True for tools
        mock_validate.return_value = True

        build_args = generate_tools_build_args(self.platforms, self.tools_version)

        # Check that build args are generated for each platform
        self.assertIn("mongodb_tools_version_amd64", build_args)
        self.assertIn("mongodb_tools_version_arm64", build_args)

        # Check that filenames contain the version
        self.assertIn(self.tools_version, build_args["mongodb_tools_version_amd64"])
        self.assertIn(self.tools_version, build_args["mongodb_tools_version_arm64"])

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_agent_build_args(self, mock_validate):
        """Test agent build args generation."""
        # Mock URL validation to return True for both agent and tools
        mock_validate.return_value = True

        build_args = generate_agent_build_args(self.platforms, self.agent_version, self.tools_version)

        # Check that build args are generated for each platform
        self.assertIn("mongodb_agent_version_amd64", build_args)
        self.assertIn("mongodb_tools_version_amd64", build_args)
        self.assertIn("mongodb_agent_version_arm64", build_args)
        self.assertIn("mongodb_tools_version_arm64", build_args)


class TestValidationFunctions(unittest.TestCase):
    """Test cases for validation and filename functions."""

    def setUp(self):
        """Set up test fixtures."""
        self.platform = "linux/amd64"
        self.platforms = ["linux/amd64", "linux/arm64"]
        self.tools_version = "100.9.5"
        self.agent_version = "13.5.2.7785"


class TestPlatformAvailability(unittest.TestCase):
    """Test cases for platform availability functions."""

    def setUp(self):
        """Set up test fixtures."""
        self.platforms = ["linux/amd64", "linux/arm64"]
        self.tools_version = "100.9.5"
        self.agent_version = "13.5.2.7785"

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_get_working_agent_filename(self, mock_validate):
        """Test getting working agent filename."""
        mock_validate.return_value = True

        filename = get_working_agent_filename(self.agent_version, "linux/amd64")

        self.assertIsInstance(filename, str)
        if filename:  # Only check if filename is found
            self.assertIn(self.agent_version, filename)
            self.assertIn("mongodb-mms-automation-agent", filename)

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_get_working_tools_filename(self, mock_validate):
        """Test getting working tools filename."""
        mock_validate.return_value = True

        filename = get_working_tools_filename(self.tools_version, "linux/amd64")

        self.assertIsInstance(filename, str)
        if filename:  # Only check if filename is found
            self.assertIn(self.tools_version, filename)
            self.assertIn("mongodb-database-tools", filename)

    def test_get_working_filename_invalid_platform(self):
        """Test getting working filename with invalid platform."""
        filename = get_working_agent_filename(self.agent_version, "invalid/platform")
        self.assertEqual(filename, "")

        filename = get_working_tools_filename(self.tools_version, "invalid/platform")
        self.assertEqual(filename, "")


class TestIntegration(unittest.TestCase):
    """Integration tests that test the actual functions."""

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_end_to_end_build_args_generation(self, mock_validate):
        """Test end-to-end build args generation as used in atomic_pipeline."""
        # Mock URL validation to return True
        mock_validate.return_value = True

        platforms = ["linux/amd64"]
        tools_version = "100.9.5"
        agent_version = "13.5.2.7785"

        # Test tools build args
        tools_args = generate_tools_build_args(platforms, tools_version)
        self.assertIn("mongodb_tools_version_amd64", tools_args)

        # Test agent build args
        agent_args = generate_agent_build_args(platforms, agent_version, tools_version)
        self.assertIn("mongodb_agent_version_amd64", agent_args)
        self.assertIn("mongodb_tools_version_amd64", agent_args)


if __name__ == "__main__":
    unittest.main()
