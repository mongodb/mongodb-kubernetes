#!/usr/bin/env python3
"""
Tests for scripts.release.agent.agents_to_rebuild.py
"""
import unittest
from unittest.mock import mock_open, patch

from scripts.release.agent.agents_to_rebuild import (
    get_all_agents_for_rebuild,
    get_currently_used_agents,
    get_evergreen_om_version_anchors,
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
    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_currently_used_agents(self, mock_load, mock_anchors):
        """Currently used agents = anchored OM versions + cloud_manager + default agentVersion."""
        mock_load.return_value = {
            "agentVersion": "108.0.12.8846-1",
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                        "ops_manager": {
                            "7.0.23": {"agent_version": "107.0.23.8833-1", "tools_version": "100.15.0"},
                            "8.0.25": {"agent_version": "108.0.25.9029-1", "tools_version": "100.17.0"},
                        },
                    }
                }
            },
        }
        mock_anchors.return_value = {"ops_manager_70_latest": "7.0.23", "ops_manager_80_latest": "8.0.25"}

        result = get_currently_used_agents()

        self.assertIn(("107.0.23.8833-1", "100.15.0"), result)
        self.assertIn(("108.0.25.9029-1", "100.17.0"), result)
        self.assertIn(("13.37.0.9590-1", "100.12.2"), result)
        self.assertIn(("108.0.12.8846-1", "100.12.2"), result)


if __name__ == "__main__":
    unittest.main()
