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
    get_evergreen_om_version_anchors,
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

    @patch("builtins.open", new_callable=mock_open)
    def test_get_evergreen_om_version_anchors(self, mock_file):
        """Test parsing .evergreen.yml for ops_manager anchor -> OM version mapping."""
        mock_file.return_value.read.return_value = (
            "variables:\n"
            "  - &ops_manager_60_latest 6.0.27 # comment\n"
            "  - &ops_manager_70_latest 7.0.23 # comment\n"
            "  - &ops_manager_80_latest 8.0.25 # comment\n"
        )
        result = get_evergreen_om_version_anchors()
        self.assertEqual(result["ops_manager_60_latest"], "6.0.27")
        self.assertEqual(result["ops_manager_70_latest"], "7.0.23")
        self.assertEqual(result["ops_manager_80_latest"], "8.0.25")

    @patch("scripts.release.agent.agents_to_rebuild.get_evergreen_om_version_anchors")
    @patch("scripts.release.agent.agents_to_rebuild.glob.glob")
    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    @patch("builtins.open", new_callable=mock_open)
    @patch("os.path.isfile", return_value=True)
    def test_get_currently_used_agents_dynamic_custom_om(
        self, mock_isfile, mock_file, mock_load, mock_glob, mock_anchors
    ):
        """Dynamic CUSTOM_OM_VERSION (om80 anchor pattern) resolves to mapped agent."""
        release_data = {
            "agentVersion": "108.0.25.9029-1",
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                        "ops_manager": {
                            "8.0.25": {
                                "agent_version": "108.0.25.9029-1",
                                "tools_version": "100.17.0",
                            },
                        },
                    }
                }
            },
        }
        mock_load.return_value = release_data
        mock_glob.return_value = ["scripts/dev/contexts/variables/om80"]
        mock_anchors.return_value = {"ops_manager_80_latest": "8.0.25"}
        mock_file.return_value.read.return_value = (
            'CUSTOM_OM_VERSION=$(grep -E "&ops_manager_80_latest" '
            "< \"${PROJECT_DIR}\"/.evergreen.yml | awk '{print $3}')\n"
            "export CUSTOM_OM_VERSION\n"
        )

        result = get_currently_used_agents()

        self.assertIn(("108.0.25.9029-1", "100.17.0"), result)

    @patch("scripts.release.agent.agents_to_rebuild.get_evergreen_om_version_anchors")
    @patch("scripts.release.agent.agents_to_rebuild.glob.glob")
    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    @patch("builtins.open", new_callable=mock_open)
    @patch("os.path.isfile", return_value=True)
    def test_get_currently_used_agents_static_custom_om_no_export(
        self, mock_isfile, mock_file, mock_load, mock_glob, mock_anchors
    ):
        """Static CUSTOM_OM_VERSION without export prefix (om60 pattern) resolves to mapped agent."""
        release_data = {
            "agentVersion": "12.0.35.7911-1",
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                        "ops_manager": {
                            "6.0.27": {
                                "agent_version": "12.0.35.7911-1",
                                "tools_version": "100.10.0",
                            },
                        },
                    }
                }
            },
        }
        mock_load.return_value = release_data
        mock_glob.return_value = ["scripts/dev/contexts/variables/om60"]
        mock_anchors.return_value = {}
        mock_file.return_value.read.return_value = "CUSTOM_OM_VERSION=6.0.27\n" "export CUSTOM_OM_VERSION\n"

        result = get_currently_used_agents()

        self.assertIn(("12.0.35.7911-1", "100.10.0"), result)


if __name__ == "__main__":
    unittest.main()
