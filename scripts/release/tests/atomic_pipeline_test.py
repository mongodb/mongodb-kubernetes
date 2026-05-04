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
from scripts.release.atomic_pipeline import resolve_agent_platforms


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


class TestBuildArgumentFailFast(unittest.TestCase):
    """Build-arg generation must fail fast when a requested platform cannot be satisfied."""

    def setUp(self):
        self.platforms = ["linux/amd64", "linux/s390x"]
        self.tools_version = "100.9.5"
        self.agent_version = "13.5.2.7785"

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_tools_build_args_raises_when_platform_unresolvable(self, mock_validate):
        # Tools available for amd64 but not s390x.
        mock_validate.side_effect = lambda url, timeout=30: "s390x" not in url

        with self.assertRaisesRegex(RuntimeError, r"linux/s390x"):
            generate_tools_build_args(self.platforms, self.tools_version)

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_agent_build_args_raises_when_agent_missing_for_platform(self, mock_validate):
        # Agent missing for s390x (matches the cloud_manager 13.51.0.10584-1 regression).
        def side_effect(url, timeout=30):
            if "automation-agent" in url and "s390x" in url:
                return False
            return True

        mock_validate.side_effect = side_effect

        with self.assertRaisesRegex(RuntimeError, r"agent.*linux/s390x"):
            generate_agent_build_args(self.platforms, self.agent_version, self.tools_version)

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_agent_build_args_raises_when_tools_missing_for_platform(self, mock_validate):
        def side_effect(url, timeout=30):
            if "database-tools" in url and "s390x" in url:
                return False
            return True

        mock_validate.side_effect = side_effect

        with self.assertRaisesRegex(RuntimeError, r"tools.*linux/s390x"):
            generate_agent_build_args(self.platforms, self.agent_version, self.tools_version)

    def test_generate_tools_build_args_raises_for_unknown_platform(self):
        with self.assertRaisesRegex(RuntimeError, r"unknown/arch"):
            generate_tools_build_args(["unknown/arch"], self.tools_version)

    def test_generate_agent_build_args_raises_for_unknown_platform(self):
        with self.assertRaisesRegex(RuntimeError, r"unknown/arch"):
            generate_agent_build_args(["unknown/arch"], self.agent_version, self.tools_version)


class TestResolveAgentPlatforms(unittest.TestCase):
    """resolve_agent_platforms drops linux/s390x for Cloud Manager (13.x) agents only."""

    def test_strips_s390x_for_cloud_manager(self):
        result = resolve_agent_platforms(
            "13.51.0.10584-1", ["linux/amd64", "linux/arm64", "linux/s390x", "linux/ppc64le"]
        )

        self.assertEqual(result, ["linux/amd64", "linux/arm64", "linux/ppc64le"])

    def test_keeps_all_for_non_cloud_manager(self):
        platforms = ["linux/amd64", "linux/arm64", "linux/s390x", "linux/ppc64le"]

        result = resolve_agent_platforms("14.0.0.7785", platforms)

        self.assertEqual(result, platforms)

    def test_cloud_manager_without_s390x_is_unchanged(self):
        platforms = ["linux/amd64", "linux/arm64", "linux/ppc64le"]

        result = resolve_agent_platforms("13.51.0.10584-1", platforms)

        self.assertEqual(result, platforms)


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
