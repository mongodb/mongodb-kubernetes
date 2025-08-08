import os
import subprocess
import unittest
from unittest.mock import patch

import pytest

from pipeline import (
    calculate_images_to_build,
    gather_all_supported_agent_versions,
    gather_latest_agent_versions,
    get_versions_to_rebuild,
    get_versions_to_rebuild_per_operator_version,
    is_version_in_range,
    operator_build_configuration,
)
from scripts.release.build.image_signing import run_command_with_retries

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
                    "7.0.10": {"agent_version": "107.0.10.8627-1", "tools_version": "100.9.5"},
                    "7.0.11": {"agent_version": "107.0.11.8645-1", "tools_version": "100.10.0"},
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
        # Given an include, it should only return include images
        (["cli", "ops-manager", "appdb-daily", "init-appdb"], ["cli"], [], {"cli"}),
        # Given no include nor excludes it should return all images
        (
            ["cli", "ops-manager", "appdb-daily", "init-appdb"],
            [],
            [],
            {"init-appdb", "appdb-daily", "ops-manager", "cli"},
        ),
        # Given an exclude, it should return all images except the excluded ones
        (
            ["cli", "ops-manager", "appdb-daily", "init-appdb"],
            [],
            ["init-appdb", "appdb-daily"],
            {"ops-manager", "cli"},
        ),
        # Given an include and a different exclude, it should return all images except the exclusions
        (
            ["cli", "ops-manager", "appdb-daily", "init-appdb"],
            ["appdb-daily"],
            ["init-appdb"],
            {"appdb-daily", "cli", "ops-manager"},
        ),
        # Given multiple includes and a different exclude, it should return all images except the exclusions
        (
            ["cli", "ops-manager", "appdb-daily", "init-appdb"],
            ["cli", "appdb-daily"],
            ["init-appdb"],
            {"appdb-daily", "cli", "ops-manager"},
        ),
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
    latest_agents = gather_latest_agent_versions(release_json)
    expected_agents = [
        ("107.0.11.8645-1", "100.10.0"),
        # TODO: Remove this once we don't need to use OM 7.0.12 in the OM Multicluster DR tests
        # https://jira.mongodb.org/browse/CLOUDP-297377
        ("107.0.12.8669-1", "100.10.0"),
        ("12.0.31.7825-1", "100.9.4"),
        ("13.19.0.8937-1", "100.9.4"),
    ]
    assert latest_agents == expected_agents


def test_get_versions_to_rebuild_same_version():
    supported_versions = gather_all_supported_agent_versions(release_json)
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


def test_get_versions_to_rebuild_multiple_versions_per_operator():
    supported_versions = ["107.0.1.8507-1_1.27.0", "107.0.1.8507-1_1.28.0", "100.0.1.8507-1_1.28.0"]
    expected_agents = ["107.0.1.8507-1_1.28.0", "100.0.1.8507-1_1.28.0"]
    agents = get_versions_to_rebuild_per_operator_version(supported_versions, "1.28.0")
    assert agents == expected_agents


def test_get_versions_to_rebuild_multiple_versions_per_operator_only_non_suffixed():
    supported_versions = ["107.0.1.8507-1_1.27.0", "107.0.10.8627-1-arm64", "100.0.10.8627-1"]
    expected_agents = ["107.0.10.8627-1-arm64", "100.0.10.8627-1"]
    agents = get_versions_to_rebuild_per_operator_version(supported_versions, "onlyAgents")
    assert agents == expected_agents


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


@patch("pipeline.shutil.which", return_value="/mock/path/to/docker")
@patch("subprocess.run")
def test_create_and_push_manifest_success(mock_run, mock_which):
    """Test successful creation and pushing of manifest with multiple architectures."""
    # Setup mock to return success for both calls
    mock_run.return_value = subprocess.CompletedProcess(args=[], returncode=0, stdout=b"", stderr=b"")

    image = "test/image"
    tag = "1.0.0"
    architectures = ["amd64", "arm64"]

    from pipeline import create_and_push_manifest

    create_and_push_manifest(image, tag, architectures)

    assert mock_run.call_count == 2

    # Verify first call - create manifest
    create_call_args = mock_run.call_args_list[0][0][0]
    assert create_call_args == [
        "/mock/path/to/docker",
        "manifest",
        "create",
        "test/image:1.0.0",
        "--amend",
        "test/image:1.0.0-amd64",
        "--amend",
        "test/image:1.0.0-arm64",
    ]

    # Verify second call - push manifest
    push_call_args = mock_run.call_args_list[1][0][0]
    assert push_call_args == ["/mock/path/to/docker", "manifest", "push", f"{image}:{tag}"]


@patch("pipeline.shutil.which", return_value="/mock/path/to/docker")
@patch("subprocess.run")
def test_create_and_push_manifest_single_arch(mock_run, mock_which):
    """Test manifest creation with a single architecture."""
    # Setup mock to return success for both calls
    mock_run.return_value = subprocess.CompletedProcess(args=[], returncode=0, stdout=b"", stderr=b"")

    image = "test/image"
    tag = "1.0.0"
    architectures = ["amd64"]

    from pipeline import create_and_push_manifest

    create_and_push_manifest(image, tag, architectures)

    # Verify first call - create manifest (should only include one architecture)
    create_call_args = mock_run.call_args_list[0][0][0]
    assert (
        " ".join(create_call_args)
        == "/mock/path/to/docker manifest create test/image:1.0.0 --amend test/image:1.0.0-amd64"
    )


@patch("pipeline.shutil.which", return_value="/mock/path/to/docker")
@patch("subprocess.run")
def test_create_and_push_manifest_create_error(mock_run, mock_which):
    """Test error handling when manifest creation fails."""
    # Setup mock to return error for create call
    mock_run.return_value = subprocess.CompletedProcess(
        args=[], returncode=1, stdout=b"", stderr=b"Error creating manifest"
    )

    image = "test/image"
    tag = "1.0.0"
    architectures = ["amd64", "arm64"]

    from pipeline import create_and_push_manifest

    # Verify exception is raised with the stderr content
    with pytest.raises(Exception) as exc_info:
        create_and_push_manifest(image, tag, architectures)

    assert "Error creating manifest" in str(exc_info.value)
    assert mock_run.call_count == 1  # Only the create call, not the push call


@patch("pipeline.shutil.which", return_value="/mock/path/to/docker")
@patch("subprocess.run")
def test_create_and_push_manifest_push_error(mock_run, mock_which):
    """Test error handling when manifest push fails."""
    # Setup mock to return success for create but error for push
    mock_run.side_effect = [
        subprocess.CompletedProcess(args=[], returncode=0, stdout=b"", stderr=b""),  # create success
        subprocess.CompletedProcess(args=[], returncode=1, stdout=b"", stderr=b"Push failed"),  # push fails
    ]

    # Call function with test parameters
    image = "test/image"
    tag = "1.0.0"
    architectures = ["amd64", "arm64"]

    from pipeline import create_and_push_manifest

    # Verify exception is raised with the stderr content
    with pytest.raises(Exception) as exc_info:
        create_and_push_manifest(image, tag, architectures)

    # The function raises the stderr directly, so we should check for the exact error message
    assert "Push failed" in str(exc_info.value)
    assert mock_run.call_count == 2  # Both create and push calls
