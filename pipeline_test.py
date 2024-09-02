import os
import subprocess
import unittest
from unittest.mock import patch

import pytest

from pipeline import (
    build_agent_gather_versions,
    build_latest_agent_versions,
    calculate_images_to_build,
    get_versions_to_rebuild,
    is_version_in_range,
    operator_build_configuration,
)
from scripts.evergreen.release.images_signing import run_command_with_retries

release_json = {
    "supportedImages": {
        "mongodb-agent": {
            "opsManagerMapping": {
                "cloud_manager": "13.19.0.8937-1",
                "cloud_manager_tools": "100.9.4",
                "ops_manager": {
                    "6.0.0": {"agent_version": "12.0.30.7791-1", "tools_version": "100.9.4"},
                    "6.0.21": {"agent_version": "12.0.29.7785-1", "tools_version": "100.9.4"},
                    "6.0.22": {"agent_version": "12.0.30.7791-1", "tools_version": "100.9.4"},
                    "7.0.0": {"agent_version": "107.0.1.8507-1", "tools_version": "100.9.4"},
                    "7.0.1": {"agent_version": "107.0.1.8507-1", "tools_version": "100.9.4"},
                    "7.0.2": {"agent_version": "107.0.2.8531-1", "tools_version": "100.9.4"},
                    "7.0.3": {"agent_version": "107.0.3.8550-1", "tools_version": "100.9.4"},
                    "6.0.23": {"agent_version": "12.0.31.7825-1", "tools_version": "100.9.4"},
                    "7.0.4": {"agent_version": "107.0.4.8567-1", "tools_version": "100.9.4"},
                    "7.0.6": {"agent_version": "107.0.6.8587-1", "tools_version": "100.9.4"},
                    "7.0.7": {"agent_version": "107.0.7.8596-1", "tools_version": "100.9.4"},
                },
            }
        }
    }
}


def test_operator_build_configuration():
    with patch.dict(os.environ, {"distro": "a_distro", "BASE_REPO_URL": "somerepo/url", "namespace": "something"}):
        config = operator_build_configuration("builder", True, False)
        assert config.image_type == "a_distro"
        assert config.base_repository == "somerepo/url"
        assert config.namespace == "something"


def test_operator_build_configuration_defaults():
    with patch.dict(
        os.environ,
        {
            "BASE_REPO_URL": "",
        },
    ):
        config = operator_build_configuration("builder", True, False)
        assert config.image_type == "ubi"
        assert config.base_repository == ""
        assert config.namespace == "default"


@pytest.mark.parametrize(
    "test_case",
    [
        (["a", "b", "c"], ["a"], ["b"], {"a", "c"}),
        (["a", "b", "c"], ["a", "b"], None, {"a", "b"}),
        (["a", "b", "c"], None, ["a"], {"b", "c"}),
        (["a", "b", "c"], [], [], {"a", "b", "c"}),
        (["a", "b", "c"], ["d"], None, ValueError),
        (["a", "b", "c"], None, ["d"], ValueError),
        ([], ["a"], ["b"], ValueError),
        (["a", "b", "c"], None, None, {"a", "b", "c"}),
    ],
)
def test_calculate_images_to_build(test_case):
    images, include, exclude, expected = test_case
    if expected is ValueError:
        with pytest.raises(ValueError):
            calculate_images_to_build(images, include, exclude)
    else:
        assert calculate_images_to_build(images, include, exclude) == expected


@pytest.mark.parametrize(
    "version,min_version,max_version,expected",
    [
        # When one bound is empty or None, always return True
        ("7.0.0", "8.0.0", "", True),
        ("7.0.0", "8.0.0", None, True),
        ("9.0.0", "", "8.0.0", True),
        # Upper bound is excluded
        ("8.1.1", "8.0.0", "8.1.1", False),
        # Lower bound is included
        ("8.0.0", "8.0.0", "8.1.1", True),
        # Test some values
        ("8.5.2", "8.5.1", "8.5.3", True),
        ("8.5.2", "8.5.3", "8.4.2", False),
    ],
)
def test_is_version_in_range(version, min_version, max_version, expected):
    assert is_version_in_range(version, min_version, max_version) == expected


@pytest.mark.parametrize(
    "description,case",
    [
        ("No skip or include tags", {"skip_tags": [], "include_tags": [], "expected": True}),
        ("Include 'release' only", {"skip_tags": [], "include_tags": ["release"], "expected": True}),
        ("Skip 'release' only", {"skip_tags": ["release"], "include_tags": [], "expected": False}),
        ("Include non-release, no skip", {"skip_tags": [], "include_tags": ["test", "deploy"], "expected": False}),
        ("Skip non-release, no include", {"skip_tags": ["test", "deploy"], "include_tags": [], "expected": True}),
        ("Include and skip 'release'", {"skip_tags": ["release"], "include_tags": ["release"], "expected": False}),
        (
            "Skip non-release, include 'release'",
            {"skip_tags": ["test", "deploy"], "include_tags": ["release"], "expected": True},
        ),
    ],
)
def test_is_release_step_executed(description, case):
    config = operator_build_configuration("builder", True, False)
    config.skip_tags = case["skip_tags"]
    config.include_tags = case["include_tags"]
    result = config.is_release_step_executed()
    assert result == case["expected"], f"Test failed: {description}. Expected {case['expected']}, got {result}."


def test_build_latest_agent_versions():
    latest_agents = build_latest_agent_versions(release_json)
    expected_agents = [("107.0.7.8596-1", "100.9.4"), ("12.0.31.7825-1", "100.9.4"), ("13.19.0.8937-1", "100.9.4")]
    assert latest_agents == expected_agents


def test_get_versions_to_rebuild_same_version():
    supported_versions = build_agent_gather_versions(release_json)
    agents = get_versions_to_rebuild(supported_versions, "6.0.0_1.26.0", "6.0.0_1.26.0")
    assert len(agents) == 1
    assert agents[0] == "6.0.0_1.26.0"


def test_get_versions_to_rebuild_multiple_versions():
    supported_versions = ["6.0.0", "6.0.1", "6.0.21", "6.11.0", "7.0.0"]
    expected_agents = ["6.0.0", "6.0.1", "6.0.21"]
    agents = get_versions_to_rebuild(supported_versions, "6.0.0", "6.10.0")
    actual_agents = []
    for a in agents:
        actual_agents.append(a)
    assert actual_agents == expected_agents


command = ["echo", "Hello World"]


class TestRunCommandWithRetries(unittest.TestCase):
    @patch("subprocess.run")
    @patch("time.sleep", return_value=None)  # to avoid actual sleeping during tests
    def test_successful_command(self, mock_sleep, mock_run):
        # Mock a successful command execution
        mock_run.return_value = subprocess.CompletedProcess(command, 0, stdout="Hello World", stderr="")

        result = run_command_with_retries(command)
        self.assertEqual(result.stdout, "Hello World")
        mock_run.assert_called_once()
        mock_sleep.assert_not_called()

    @patch("subprocess.run")
    @patch("time.sleep", return_value=None)  # to avoid actual sleeping during tests
    def test_retryable_error(self, mock_sleep, mock_run):
        # Mock a retryable error first, then a successful command execution
        mock_run.side_effect = [
            subprocess.CalledProcessError(500, command, stderr="500 Internal Server Error"),
            subprocess.CompletedProcess(command, 0, stdout="Hello World", stderr=""),
        ]

        result = run_command_with_retries(command)
        self.assertEqual(result.stdout, "Hello World")
        self.assertEqual(mock_run.call_count, 2)
        mock_sleep.assert_called_once()

    @patch("subprocess.run")
    @patch("time.sleep", return_value=None)  # to avoid actual sleeping during tests
    def test_non_retryable_error(self, mock_sleep, mock_run):
        # Mock a non-retryable error
        mock_run.side_effect = subprocess.CalledProcessError(1, command, stderr="1 Some Error")

        with self.assertRaises(subprocess.CalledProcessError):
            run_command_with_retries(command)

        self.assertEqual(mock_run.call_count, 1)
        mock_sleep.assert_not_called()

    @patch("subprocess.run")
    @patch("time.sleep", return_value=None)  # to avoid actual sleeping during tests
    def test_all_retries_fail(self, mock_sleep, mock_run):
        # Mock all attempts to fail with a retryable error
        mock_run.side_effect = subprocess.CalledProcessError(500, command, stderr="500 Internal Server Error")

        with self.assertRaises(subprocess.CalledProcessError):
            run_command_with_retries(command, retries=3)

        self.assertEqual(mock_run.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)
