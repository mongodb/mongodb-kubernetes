import os
import subprocess
from unittest.mock import MagicMock, patch

import pytest

from scripts.release.branch_detection import (
    get_cache_scope,
    get_current_branch,
    get_version_id,
    is_evg_patch,
    is_running_in_evg,
)


class TestGetCurrentBranch:
    """Test branch detection logic for different scenarios."""

    @patch("scripts.release.branch_detection.is_running_in_evg")
    @patch("subprocess.run")
    def test_local_development_master_branch(self, mock_run, mock_is_evg):
        """Test local development on master branch."""
        mock_is_evg.return_value = False
        mock_run.return_value = MagicMock(stdout="master\n", returncode=0)

        result = get_current_branch()

        assert result == "master"
        mock_run.assert_called_once_with(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"], capture_output=True, text=True, check=True
        )

    @patch("scripts.release.branch_detection.is_running_in_evg")
    @patch("subprocess.run")
    def test_local_development_feature_branch(self, mock_run, mock_is_evg):
        """Test local development on feature branch."""
        mock_is_evg.return_value = False
        mock_run.return_value = MagicMock(stdout="feature/new-cache\n", returncode=0)

        result = get_current_branch()

        assert result == "feature/new-cache"

    @patch("scripts.release.branch_detection.is_running_in_evg")
    @patch("subprocess.run")
    def test_local_development_detached_head(self, mock_run, mock_is_evg):
        """Test local development in detached HEAD state."""
        mock_is_evg.return_value = False
        mock_run.return_value = MagicMock(stdout="HEAD\n", returncode=0)

        result = get_current_branch()

        assert result == "master"  # fallback to master

    @patch("scripts.release.branch_detection.is_running_in_evg")
    @patch("subprocess.run")
    def test_local_development_git_error(self, mock_run, mock_is_evg):
        """Test local development when git command fails."""
        mock_is_evg.return_value = False
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = get_current_branch()

        assert result == "master"  # fallback to master

    @patch("scripts.release.branch_detection.is_running_in_evg")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("subprocess.run")
    def test_evergreen_non_patch_build(self, mock_run, mock_is_patch, mock_is_evg):
        """Test Evergreen non-patch build."""
        mock_is_evg.return_value = True
        mock_is_patch.return_value = False
        mock_run.return_value = MagicMock(stdout="master\n", returncode=0)

        result = get_current_branch()

        assert result == "master"

    @patch("scripts.release.branch_detection.is_running_in_evg")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("subprocess.run")
    def test_evergreen_patch_build_branch_detection(self, mock_run, mock_is_patch, mock_is_evg):
        """Test Evergreen patch build with successful branch detection."""
        mock_is_evg.return_value = True
        mock_is_patch.return_value = True

        # Mock git for-each-ref output
        mock_run.side_effect = [
            MagicMock(
                stdout="origin/feature/cache-improvement abc123\norigin/evg-pr-test-123 abc123\norigin/main def456\n",
                returncode=0,
            ),
            MagicMock(stdout="abc123\n", returncode=0),
        ]

        result = get_current_branch()

        assert result == "feature/cache-improvement"

    @patch("scripts.release.branch_detection.is_running_in_evg")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("subprocess.run")
    def test_evergreen_patch_build_fallback(self, mock_run, mock_is_patch, mock_is_evg):
        """Test Evergreen patch build fallback when branch detection fails."""
        mock_is_evg.return_value = True
        mock_is_patch.return_value = True
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = get_current_branch()

        assert result == "master"  # fallback to master


class TestGetCacheScope:
    """Test cache scope generation for different scenarios."""

    @patch("scripts.release.branch_detection.get_current_branch")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("scripts.release.branch_detection.get_version_id")
    def test_master_branch_non_patch(self, mock_version_id, mock_is_patch, mock_branch):
        """Test cache scope for master branch non-patch build."""
        mock_branch.return_value = "master"
        mock_is_patch.return_value = False
        mock_version_id.return_value = None

        result = get_cache_scope()

        assert result == "master"

    @patch("scripts.release.branch_detection.get_current_branch")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("scripts.release.branch_detection.get_version_id")
    def test_feature_branch_non_patch(self, mock_version_id, mock_is_patch, mock_branch):
        """Test cache scope for feature branch non-patch build."""
        mock_branch.return_value = "feature/new-cache"
        mock_is_patch.return_value = False
        mock_version_id.return_value = None

        result = get_cache_scope()

        assert result == "feature-new-cache"

    @patch("scripts.release.branch_detection.get_current_branch")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("scripts.release.branch_detection.get_version_id")
    def test_patch_build_with_version_id(self, mock_version_id, mock_is_patch, mock_branch):
        """Test cache scope for patch build with version ID."""
        mock_branch.return_value = "feature/new-cache"
        mock_is_patch.return_value = True
        mock_version_id.return_value = "6899b7e35bfaee00077db986"

        result = get_cache_scope()

        assert result == "feature-new-cache-6899b7e3"

    @patch("scripts.release.branch_detection.get_current_branch")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("scripts.release.branch_detection.get_version_id")
    def test_patch_build_without_version_id(self, mock_version_id, mock_is_patch, mock_branch):
        """Test cache scope for patch build without version ID."""
        mock_branch.return_value = "feature/new-cache"
        mock_is_patch.return_value = True
        mock_version_id.return_value = None

        result = get_cache_scope()

        assert result == "feature-new-cache"

    @patch("scripts.release.branch_detection.get_current_branch")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("scripts.release.branch_detection.get_version_id")
    def test_branch_name_sanitization(self, mock_version_id, mock_is_patch, mock_branch):
        """Test branch name sanitization for cache scope."""
        mock_branch.return_value = "Feature/NEW_cache@123"
        mock_is_patch.return_value = False
        mock_version_id.return_value = None

        result = get_cache_scope()

        assert result == "feature-new_cache-123"

    @patch("scripts.release.branch_detection.get_current_branch")
    @patch("scripts.release.branch_detection.is_evg_patch")
    @patch("scripts.release.branch_detection.get_version_id")
    def test_dependabot_branch(self, mock_version_id, mock_is_patch, mock_branch):
        """Test cache scope for dependabot branch."""
        mock_branch.return_value = "dependabot/npm_and_yarn/lodash-4.17.21"
        mock_is_patch.return_value = False
        mock_version_id.return_value = None

        result = get_cache_scope()

        assert result == "dependabot-npm_and_yarn-lodash-4.17.21"


class TestEnvironmentDetection:
    """Test environment detection functions."""

    def test_is_evg_patch_true(self):
        """Test is_evg_patch returns True when is_patch is 'true'."""
        with patch.dict(os.environ, {"is_patch": "true"}):
            assert is_evg_patch() is True

    def test_is_evg_patch_false(self):
        """Test is_evg_patch returns False when is_patch is 'false'."""
        with patch.dict(os.environ, {"is_patch": "false"}):
            assert is_evg_patch() is False

    def test_is_evg_patch_default(self):
        """Test is_evg_patch returns False when is_patch is not set."""
        with patch.dict(os.environ, {}, clear=True):
            assert is_evg_patch() is False

    def test_is_running_in_evg_true(self):
        """Test is_running_in_evg returns True when RUNNING_IN_EVG is 'true'."""
        with patch.dict(os.environ, {"RUNNING_IN_EVG": "true"}):
            assert is_running_in_evg() is True

    def test_is_running_in_evg_false(self):
        """Test is_running_in_evg returns False when RUNNING_IN_EVG is 'false'."""
        with patch.dict(os.environ, {"RUNNING_IN_EVG": "false"}):
            assert is_running_in_evg() is False

    def test_is_running_in_evg_default(self):
        """Test is_running_in_evg returns False when RUNNING_IN_EVG is not set."""
        with patch.dict(os.environ, {}, clear=True):
            assert is_running_in_evg() is False

    def test_get_version_id_set(self):
        """Test get_version_id returns value when version_id is set."""
        with patch.dict(os.environ, {"version_id": "6899b7e35bfaee00077db986"}):
            assert get_version_id() == "6899b7e35bfaee00077db986"

    def test_get_version_id_not_set(self):
        """Test get_version_id returns None when version_id is not set."""
        with patch.dict(os.environ, {}, clear=True):
            assert get_version_id() is None
