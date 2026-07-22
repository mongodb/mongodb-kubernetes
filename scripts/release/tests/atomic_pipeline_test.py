#!/usr/bin/env python3
"""
Test for agent build mapping functionality and validation functions.
"""

import unittest
from unittest.mock import MagicMock, patch

from scripts.release.agent.validation import (
    generate_agent_build_args,
    generate_tools_build_args,
    get_working_agent_filename,
    get_working_tools_filename,
)
from scripts.release.atomic_pipeline import get_custom_agent_url, get_custom_agent_url_for_version


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


class TestBuildArgumentSkipsUnresolvable(unittest.TestCase):
    """Build-arg generation skips platforms whose agent or tools tarball cannot be resolved
    (e.g. when an old agent's S3 artifact has aged out). Callers handle the consequences."""

    def setUp(self):
        self.platforms = ["linux/amd64", "linux/s390x"]
        self.tools_version = "100.9.5"
        self.agent_version = "13.5.2.7785"

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_tools_build_args_skips_unresolvable_platform(self, mock_validate):
        mock_validate.side_effect = lambda url, timeout=30: "s390x" not in url

        build_args = generate_tools_build_args(self.platforms, self.tools_version)

        self.assertIn("mongodb_tools_version_amd64", build_args)
        self.assertNotIn("mongodb_tools_version_s390x", build_args)

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_agent_build_args_skips_when_agent_missing_for_platform(self, mock_validate):
        def side_effect(url, timeout=30):
            if "automation-agent" in url and "s390x" in url:
                return False
            return True

        mock_validate.side_effect = side_effect

        build_args = generate_agent_build_args(self.platforms, self.agent_version, self.tools_version)

        self.assertIn("mongodb_agent_version_amd64", build_args)
        self.assertIn("mongodb_tools_version_amd64", build_args)
        self.assertNotIn("mongodb_agent_version_s390x", build_args)
        self.assertNotIn("mongodb_tools_version_s390x", build_args)

    @patch("scripts.release.agent.validation._validate_url_exists")
    def test_generate_agent_build_args_skips_when_tools_missing_for_platform(self, mock_validate):
        def side_effect(url, timeout=30):
            if "database-tools" in url and "s390x" in url:
                return False
            return True

        mock_validate.side_effect = side_effect

        build_args = generate_agent_build_args(self.platforms, self.agent_version, self.tools_version)

        self.assertIn("mongodb_agent_version_amd64", build_args)
        self.assertNotIn("mongodb_agent_version_s390x", build_args)
        self.assertNotIn("mongodb_tools_version_s390x", build_args)

    def test_generate_tools_build_args_raises_for_unknown_platform(self):
        with self.assertRaisesRegex(RuntimeError, r"unknown/arch"):
            generate_tools_build_args(["unknown/arch"], self.tools_version)

    def test_generate_agent_build_args_raises_for_unknown_platform(self):
        with self.assertRaisesRegex(RuntimeError, r"unknown/arch"):
            generate_agent_build_args(["unknown/arch"], self.agent_version, self.tools_version)


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


class TestCustomAgentUrlResolution(unittest.TestCase):
    """Tests for get_custom_agent_url and get_custom_agent_url_for_version.

    Verifies the parameter precedence:
    1. release.json customAgent (manual mode)
    2. MDB_CUSTOM_AGENT_URL env var (automatic CI mode)
    3. Empty string (prod mode)
    """

    def setUp(self):
        self.agent_version = "109.0.0.9188-1"
        self.automatic_url = (
            "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/"
            "builds/patches/6a588a96814ba600072a706d/automation-agent/local/"
            "mongodb-mms-automation-agent-109.0.0.9188-1.linux_x86_64.tar.gz"
        )
        self.manual_url = (
            "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/"
            "builds/patches/abc123/automation-agent/local/"
            "mongodb-mms-automation-agent-109.0.0.9188-1.rhel8_x86_64.tar.gz"
        )

    @patch.dict("os.environ", {"MDB_CUSTOM_AGENT_URL": ""}, clear=False)
    @patch("scripts.release.atomic_pipeline.load_release_file")
    def test_manual_takes_precedence_over_env_var(self, mock_release):
        """release.json customAgent takes precedence over MDB_CUSTOM_AGENT_URL env var."""
        mock_release.return_value = {"customAgent": {"8": self.manual_url}}

        with patch.dict("os.environ", {"MDB_CUSTOM_AGENT_URL": self.automatic_url}):
            result = get_custom_agent_url(self.agent_version)

        self.assertEqual(result, self.manual_url)

    @patch.dict("os.environ", {"MDB_CUSTOM_AGENT_URL": ""}, clear=False)
    @patch("scripts.release.atomic_pipeline.load_release_file")
    def test_falls_back_to_env_var_when_no_manual(self, mock_release):
        """Without release.json customAgent, falls back to MDB_CUSTOM_AGENT_URL env var."""
        mock_release.return_value = {"customAgent": {"8": ""}}

        with patch.dict("os.environ", {"MDB_CUSTOM_AGENT_URL": self.automatic_url}):
            result = get_custom_agent_url(self.agent_version)

        self.assertEqual(result, self.automatic_url)

    @patch.dict("os.environ", {"MDB_CUSTOM_AGENT_URL": ""}, clear=False)
    @patch("scripts.release.atomic_pipeline.load_release_file")
    def test_returns_empty_when_no_custom_agent(self, mock_release):
        """Returns empty string when neither manual nor env var has a URL."""
        mock_release.return_value = {"customAgent": {"8": "", "7": ""}}

        result = get_custom_agent_url(self.agent_version)

        self.assertEqual(result, "")

    @patch.dict("os.environ", {"MDB_CUSTOM_AGENT_URL": ""}, clear=False)
    @patch("scripts.release.atomic_pipeline.load_release_file")
    def test_returns_empty_when_custom_agent_key_missing(self, mock_release):
        """Returns empty string when release.json has no customAgent key."""
        mock_release.return_value = {"agentVersion": "108.0.12.8846-1"}

        result = get_custom_agent_url(self.agent_version)

        self.assertEqual(result, "")

    @patch("scripts.release.atomic_pipeline.load_release_file")
    def test_get_custom_agent_url_for_version_matches_version_in_url(self, mock_release):
        """get_custom_agent_url_for_version returns URL containing the agent version."""
        mock_release.return_value = {
            "customAgent": {
                "8": "https://example.com/agent-109.0.0.9188-1.tar.gz",
                "7": "https://example.com/agent-107.0.0.1234-1.tar.gz",
            }
        }

        result = get_custom_agent_url_for_version("109.0.0.9188-1")

        self.assertEqual(result, "https://example.com/agent-109.0.0.9188-1.tar.gz")

    @patch("scripts.release.atomic_pipeline.load_release_file")
    def test_get_custom_agent_url_for_version_returns_empty_when_no_match(self, mock_release):
        """get_custom_agent_url_for_version returns empty when no URL matches the version."""
        mock_release.return_value = {
            "customAgent": {
                "8": "https://example.com/agent-107.0.0.1234-1.tar.gz",
            }
        }

        result = get_custom_agent_url_for_version("109.0.0.9188-1")

        self.assertEqual(result, "")


if __name__ == "__main__":
    unittest.main()
