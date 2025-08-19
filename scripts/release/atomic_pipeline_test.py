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

    def test_agent_version_validation(self):
        """Test that agent version validation works correctly."""
        from scripts.release.agent.validation import (
            validate_agent_version_exists,
            validate_tools_version_exists,
        )

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
