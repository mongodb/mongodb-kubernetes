#!/usr/bin/env python3
"""
Tests for scripts.release.agent.detect_ops_manager_changes.py
"""
import json
import subprocess
import unittest
from unittest.mock import MagicMock, mock_open, patch

from scripts.release.agent.detect_ops_manager_changes import (
    detect_ops_manager_changes,
    extract_ops_manager_mapping,
    get_content_from_git,
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

    @patch("subprocess.run")
    def test_safe_git_show_success(self, mock_run):
        """Test successful git show operation"""
        mock_result = MagicMock()
        mock_result.stdout = json.dumps(self.master_release_data)
        mock_run.return_value = mock_result

        result = get_content_from_git("abc123", "release.json")
        self.assertEqual(result, json.dumps(self.master_release_data))

        mock_run.assert_called_once_with(
            ["git", "show", "abc123:release.json"], capture_output=True, text=True, check=True, timeout=30
        )

    @patch("subprocess.run", side_effect=subprocess.CalledProcessError(1, "git"))
    def test_safe_git_show_failure(self, mock_run):
        """Test git show failure handling"""
        result = get_content_from_git("abc123", "release.json")
        self.assertIsNone(result)

    def test_no_changes_detected(self):
        """Test when no changes are detected"""
        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=self.current_release_data,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertEqual(changed_agents, [])

    def test_new_ops_manager_version_added(self):
        """Test detection when new OM version is added"""
        modified_current = json.loads(json.dumps(self.current_release_data))
        modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]["8.0.0"] = {
            "agent_version": "108.0.0.8694-1",
            "tools_version": "100.10.0",
        }

        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=modified_current,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertIn(("108.0.0.8694-1", "100.10.0"), changed_agents)

    def test_ops_manager_version_modified(self):
        """Test that modifying existing OM version is NOT detected (only new versions are detected)"""
        modified_current = json.loads(json.dumps(self.current_release_data))
        modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]["6.0.26"][
            "agent_version"
        ] = "12.0.35.7911-1"

        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=modified_current,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            # Modified existing OM versions should NOT be detected
            self.assertEqual(changed_agents, [])

    def test_cloud_manager_changed(self):
        """Test detection when cloud_manager is changed"""
        modified_current = json.loads(json.dumps(self.current_release_data))
        modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"] = "13.38.0.9600-1"

        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=modified_current,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertIn(("13.38.0.9600-1", "100.12.2"), changed_agents)

    def test_cloud_manager_tools_changed(self):
        """Test detection when cloud_manager_tools is changed"""
        modified_current = json.loads(json.dumps(self.current_release_data))
        modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager_tools"] = "100.13.0"

        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=modified_current,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertIn(("13.37.0.9590-1", "100.13.0"), changed_agents)

    def test_cloud_manager_downgrade_not_detected(self):
        """Test that cloud manager downgrade is NOT detected"""
        modified_current = json.loads(json.dumps(self.current_release_data))
        # Downgrade from 13.37.0.9590-1 to 13.36.0.9500-1
        modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"] = "13.36.0.9500-1"

        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=modified_current,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            # Downgrade should NOT be detected
            self.assertEqual(changed_agents, [])

    def test_ops_manager_version_removed(self):
        """Test detection when OM version is removed"""
        modified_current = json.loads(json.dumps(self.current_release_data))
        del modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]["7.0.11"]

        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=modified_current,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertEqual(changed_agents, [])

    def test_both_om_and_cm_changed(self):
        """Test detection when both OM version and cloud manager are changed"""
        modified_current = json.loads(json.dumps(self.current_release_data))
        modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]["8.0.0"] = {
            "agent_version": "108.0.0.8694-1",
            "tools_version": "100.10.0",
        }
        modified_current["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"] = "13.38.0.9600-1"

        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=modified_current,
            ),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertIn(("108.0.0.8694-1", "100.10.0"), changed_agents)
            self.assertIn(("13.38.0.9600-1", "100.12.2"), changed_agents)
            self.assertEqual(len(changed_agents), 2)

    def test_current_release_load_failure(self):
        """Test handling when current release.json cannot be loaded"""
        with (
            patch("scripts.release.agent.detect_ops_manager_changes.load_current_release_json", return_value=None),
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master",
                return_value=self.master_release_data,
            ),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertEqual(changed_agents, [])

    def test_base_release_load_failure_fail_safe(self):
        """Test fail-safe behavior when base release.json cannot be loaded"""
        with (
            patch(
                "scripts.release.agent.detect_ops_manager_changes.load_current_release_json",
                return_value=self.current_release_data,
            ),
            patch("scripts.release.agent.detect_ops_manager_changes.load_release_json_from_master", return_value=None),
        ):

            changed_agents = detect_ops_manager_changes()
            self.assertEqual(changed_agents, [])


if __name__ == "__main__":
    unittest.main()
