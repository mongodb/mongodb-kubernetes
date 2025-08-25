import subprocess
from unittest.mock import MagicMock, patch

from scripts.release.branch_detection import (
    get_cache_scope,
    get_current_branch,
)


class TestGetCurrentBranch:
    """Test branch detection logic for different scenarios."""

    @patch("subprocess.run")
    def test_master_branch(self, mock_run):
        """Test detection of master branch."""
        mock_run.return_value = MagicMock(stdout="master\n", returncode=0)

        result = get_current_branch()

        assert result == "master"
        mock_run.assert_called_once_with(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"], capture_output=True, text=True, check=True
        )

    @patch("subprocess.run")
    def test_feature_branch(self, mock_run):
        """Test detection of feature branch."""
        mock_run.return_value = MagicMock(stdout="feature/new-cache\n", returncode=0)

        result = get_current_branch()

        assert result == "feature/new-cache"
        mock_run.assert_called_once_with(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"], capture_output=True, text=True, check=True
        )

    @patch("subprocess.run")
    def test_detached_head(self, mock_run):
        """Test detection when in detached HEAD state."""
        mock_run.return_value = MagicMock(stdout="HEAD\n", returncode=0)

        result = get_current_branch()

        assert result == "master"  # fallback to master

    @patch("subprocess.run")
    def test_empty_output(self, mock_run):
        """Test detection when git returns empty output."""
        mock_run.return_value = MagicMock(stdout="\n", returncode=0)

        result = get_current_branch()

        assert result == "master"  # fallback to master

    @patch("subprocess.run")
    def test_git_command_fails(self, mock_run):
        """Test fallback when git command fails."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = get_current_branch()

        assert result == "master"  # fallback to master

    @patch("subprocess.run")
    def test_git_not_found(self, mock_run):
        """Test fallback when git is not found."""
        mock_run.side_effect = FileNotFoundError("git not found")

        result = get_current_branch()

        assert result == "master"  # fallback to master


class TestGetCacheScope:
    """Test cache scope generation for different scenarios."""

    @patch("scripts.release.branch_detection.get_current_branch")
    def test_feature_branch(self, mock_branch):
        """Test cache scope for feature branch."""
        mock_branch.return_value = "feature/new-cache"

        result = get_cache_scope()

        assert result == "feature-new-cache"

    @patch("scripts.release.branch_detection.get_current_branch")
    def test_branch_name_sanitization(self, mock_branch):
        """Test branch name sanitization for cache scope."""
        mock_branch.return_value = "Feature/NEW_cache@123"

        result = get_cache_scope()

        assert result == "feature-new_cache-123"

    @patch("scripts.release.branch_detection.get_current_branch")
    def test_complex_branch_name(self, mock_branch):
        """Test cache scope for complex branch name with special characters."""
        mock_branch.return_value = "user/feature-123_test.branch"

        result = get_cache_scope()

        assert result == "user-feature-123_test.branch"
