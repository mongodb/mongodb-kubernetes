#!/usr/bin/env python3
"""
Tests for scripts.release.agent.agents_to_rebuild.py
"""
import unittest
from unittest.mock import patch

from scripts.release.agent.agents_to_rebuild import (
    get_all_agents_for_rebuild,
    get_currently_used_agents,
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

    @patch("scripts.release.agent.agents_to_rebuild.get_tools_version_for_agent")
    @patch("scripts.release.agent.agents_to_rebuild.load_current_release_json")
    def test_get_currently_used_agents(self, mock_load, mock_tools):
        """Currently used agents = latest per OM major + cloud_manager + default agentVersion."""
        mock_load.return_value = {
            "agentVersion": "108.0.12.8846-1",
            "latestOpsManagerAgentMapping": [
                {"7": {"opsManagerVersion": "7.0.23", "agentVersion": "107.0.23.8833-1"}},
                {"8": {"opsManagerVersion": "8.0.25", "agentVersion": "108.0.25.9029-1"}},
            ],
            "supportedImages": {
                "mongodb-agent": {
                    "opsManagerMapping": {
                        "cloud_manager": "13.37.0.9590-1",
                        "cloud_manager_tools": "100.12.2",
                    }
                }
            },
        }
        mock_tools.side_effect = lambda v: {"107.0.23.8833-1": "100.15.0", "108.0.25.9029-1": "100.17.0"}.get(v, "100.12.2")

        result = get_currently_used_agents()

        self.assertIn(("107.0.23.8833-1", "100.15.0"), result)
        self.assertIn(("108.0.25.9029-1", "100.17.0"), result)
        self.assertIn(("13.37.0.9590-1", "100.12.2"), result)
        self.assertIn(("108.0.12.8846-1", "100.12.2"), result)


if __name__ == "__main__":
    unittest.main()
