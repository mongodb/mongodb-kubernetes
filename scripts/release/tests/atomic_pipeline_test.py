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
from scripts.release.atomic_pipeline import build_agent_pipeline
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.build.image_build_configuration import ImageBuildConfiguration


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


if __name__ == "__main__":
    unittest.main()


class TestIsolatedAgentBuild(unittest.TestCase):
    """Isolated builds always rebuild, so agents without downloadable tarballs must be skipped, not fail the build."""

    def _config(self, isolated: bool) -> ImageBuildConfiguration:
        return ImageBuildConfiguration(
            scenario=BuildScenario.PATCH,
            version="current",
            latest_tag=False,
            olm_tag=False,
            registries=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/exp-1/mongodb-agent"],
            dockerfile_path="docker/mongodb-agent/Dockerfile",
            builder=None,
            platforms=["linux/amd64"],
            isolated=isolated,
        )

    @patch("scripts.release.atomic_pipeline.build_image")
    @patch("scripts.release.atomic_pipeline.generate_agent_build_args", return_value={})
    def test_isolated_skips_agent_without_tarball(self, _mock_args, mock_build_image):
        build_agent_pipeline(self._config(isolated=True), "12.0.35.7911-1", "100.10.0")
        mock_build_image.assert_not_called()

    @patch("scripts.release.atomic_pipeline.build_image")
    @patch(
        "scripts.release.atomic_pipeline.generate_agent_build_args",
        return_value={"mongodb_agent_version_amd64": "agent.tgz", "mongodb_tools_version_amd64": "tools.tgz"},
    )
    def test_isolated_builds_agent_with_tarball(self, _mock_args, mock_build_image):
        build_agent_pipeline(self._config(isolated=True), "109.0.1.9310-1", "100.17.0")
        mock_build_image.assert_called_once()

    @patch("scripts.release.atomic_pipeline.build_image")
    @patch("scripts.release.atomic_pipeline.generate_agent_build_args", return_value={})
    def test_non_isolated_behavior_unchanged(self, _mock_args, mock_build_image):
        # Shared builds rely on skip-if-exists inside build_image; missing tarballs must not short-circuit that.
        build_agent_pipeline(self._config(isolated=False), "12.0.35.7911-1", "100.10.0")
        mock_build_image.assert_called_once()
