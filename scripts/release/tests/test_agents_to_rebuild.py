#!/usr/bin/env python3
"""
Tests for scripts.release.agent.agents_to_rebuild.py
"""
import json
import unittest
from unittest.mock import mock_open, patch

from scripts.release.agent.agents_to_rebuild import (
    extract_ops_manager_mapping,
    get_all_agents_for_rebuild,
    get_currently_used_agents,
    get_tools_version_for_agent,
    load_current_release_json,
)


class TestDetectOpsManagerChanges(unittest.TestCase):

    def setUp(self):
        """Set up test fixtures"""
        self.master_release_data = {
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                        "ops_manager": {
                            "6.0.26": {"agent_version": "12.0.34.7888-1", "tools_version": "100.10.0"},
                            "7.0.11": {"agent_version": "107.0.11.8645-1", "tools_version": "100.10.0"},
                        },
                    }
                }
            }
        }

        self.current_release_data = {
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                        "ops_manager": {
                            "6.0.26": {"agent_version": "12.0.34.7888-1", "tools_version": "100.10.0"},
                            "7.0.11": {"agent_version": "107.0.11.8645-1", "tools_version": "100.10.0"},
                        },
                    }
                }
            }
        }

        self.evergreen_content = """
variables:
  - &ops_manager_60_latest 6.0.27 # The order/index is important
  - &ops_manager_70_latest 7.0.17 # The order/index is important
  - &ops_manager_80_latest 8.0.12 # The order/index is important
"""

    def test_extract_ops_manager_mapping_valid(self):
        """Test extracting opsManagerMapping from valid release data"""
        mapping = extract_ops_manager_mapping(self.master_release_data)
        expected = {
            "cloud_manager": "13.37.0.9590-1",
            "cloud_manager_tools": "100.12.2",
            "ops_manager": {
                "6.0.26": {"agent_version": "12.0.34.7888-1", "tools_version": "100.10.0"},
                "7.0.11": {"agent_version": "107.0.11.8645-1", "tools_version": "100.10.0"},
            },
        }
        self.assertEqual(mapping, expected)

    def test_extract_ops_manager_mapping_empty(self):
        """Test extracting from empty/invalid data"""
        self.assertEqual(extract_ops_manager_mapping({}), {})
        self.assertEqual(extract_ops_manager_mapping(None), {})

    def test_extract_ops_manager_mapping_missing_keys(self):
        """Test extracting when keys are missing"""
        incomplete_data = {"supportedImages": {}}
        self.assertEqual(extract_ops_manager_mapping(incomplete_data), {})

    @patch("builtins.open", new_callable=mock_open)
    @patch("os.path.exists", return_value=True)
    def test_load_current_release_json_success(self, mock_exists, mock_file):
        """Test successfully loading current release.json"""
        mock_file.return_value.read.return_value = json.dumps(self.current_release_data)

        result = load_current_release_json()
        self.assertEqual(result, self.current_release_data)

    @patch("builtins.open", side_effect=FileNotFoundError)
    def test_load_current_release_json_not_found(self, mock_file):
        """Test handling missing release.json"""
        result = load_current_release_json()
        self.assertIsNone(result)

    @patch("builtins.open", new_callable=mock_open)
    @patch("os.path.exists", return_value=True)
    def test_load_current_release_json_invalid_json(self, mock_exists, mock_file):
        """Test handling invalid JSON in release.json"""
        mock_file.return_value.read.return_value = "invalid json"

        result = load_current_release_json()
        self.assertIsNone(result)

    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_tools_version_for_agent_found(self, mock_load):
        """Test getting tools version for a known agent version"""
        mock_load.return_value = self.current_release_data
        result = get_tools_version_for_agent("12.0.34.7888-1")
        self.assertEqual(result, "100.10.0")

    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_tools_version_for_agent_not_found(self, mock_load):
        """Test getting tools version for unknown agent falls back to cloud_manager_tools"""
        mock_load.return_value = self.current_release_data
        result = get_tools_version_for_agent("unknown-agent-version")
        self.assertEqual(result, "100.12.2")

    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_tools_version_for_agent_no_release_data(self, mock_load):
        """Test getting tools version when release.json cannot be loaded"""
        mock_load.return_value = None
        result = get_tools_version_for_agent("any-version")
        self.assertEqual(result, "100.12.2")

    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_all_agents_for_rebuild(self, mock_load):
        """Test getting all agents for rebuild from release.json"""
        release_data = {
            "agentVersion": "13.37.0.9590-1",
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                        "ops_manager": {
                            "6.0.26": {"agent_version": "12.0.34.7888-1", "tools_version": "100.10.0"},
                            "7.0.11": {"agent_version": "107.0.11.8645-1", "tools_version": "100.10.0"},
                        },
                    }
                }
            },
        }
        mock_load.return_value = release_data
        result = get_all_agents_for_rebuild()

        # Should contain ops_manager agents, cloud_manager agent, and main agent
        self.assertIn(("12.0.34.7888-1", "100.10.0"), result)
        self.assertIn(("107.0.11.8645-1", "100.10.0"), result)
        self.assertIn(("13.37.0.9590-1", "100.12.2"), result)

    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_all_agents_for_rebuild_no_release_data(self, mock_load):
        """Test getting all agents when release.json cannot be loaded"""
        mock_load.return_value = None
        result = get_all_agents_for_rebuild()
        self.assertEqual(result, [])

    @patch("scripts.release.agent.agents_to_rebuild.glob.glob")
    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_currently_used_agents_no_release_data(self, mock_load, mock_glob):
        """Test getting currently used agents when release.json cannot be loaded"""
        mock_load.return_value = None
        result = get_currently_used_agents()
        self.assertEqual(result, [])

    @patch("scripts.release.agent.agents_to_rebuild.glob.glob")
    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    @patch("builtins.open", new_callable=mock_open)
    @patch("os.path.isfile", return_value=True)
    def test_get_currently_used_agents_with_context_files(self, mock_isfile, mock_file, mock_load, mock_glob):
        """Test getting currently used agents from context files"""
        release_data = {
            "agentVersion": "13.37.0.9590-1",
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                        "ops_manager": {},
                    }
                }
            },
        }
        mock_load.return_value = release_data
        mock_glob.return_value = ["scripts/dev/contexts/test_context"]
        mock_file.return_value.read.return_value = "export AGENT_VERSION=12.0.34.7888-1\n"

        result = get_currently_used_agents()

        self.assertIn(("12.0.34.7888-1", "100.12.2"), result)  # falls back to cloud_manager_tools
        self.assertIn(("13.37.0.9590-1", "100.12.2"), result)


if __name__ == "__main__":
    unittest.main()
