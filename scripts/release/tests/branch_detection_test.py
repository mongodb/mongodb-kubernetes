import subprocess
from unittest.mock import MagicMock, patch

from scripts.release.branch_detection import get_cache_scope, get_current_branch


class TestGetCurrentBranch:
    """Test branch detection logic for different scenarios."""

    @patch("subprocess.run")
    def test_ci_environment_with_original_branch(self, mock_run):
        """Test detection of original branch in CI environment like Evergreen."""

        # Mock the sequence of git commands
        def side_effect(cmd, **kwargs):
            if cmd == ["git", "rev-parse", "HEAD"]:
                return MagicMock(stdout="4cecea664abcd1234567890\n", returncode=0)
            elif cmd == ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"]:
                return MagicMock(
                    stdout="origin/master 1234567890abcdef\norigin/add-caching 4cecea664abcd1234567890\norigin/evg-pr-test-12345 4cecea664abcd1234567890\n",
                    returncode=0,
                )
            elif cmd == ["git", "rev-parse", "--abbrev-ref", "HEAD"]:
                return MagicMock(stdout="evg-pr-test-12345\n", returncode=0)
            return MagicMock(stdout="", returncode=1)

        mock_run.side_effect = side_effect

        result = get_current_branch()

        assert result == "add-caching"

    @patch("subprocess.run")
    def test_master_branch_fallback(self, mock_run):
        """Test detection of master branch using fallback method."""

        # Mock the sequence where sophisticated method fails but fallback works
        def side_effect(cmd, **kwargs):
            if cmd == ["git", "rev-parse", "HEAD"]:
                return MagicMock(stdout="4cecea664abcd1234567890\n", returncode=0)
            elif cmd == ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"]:
                raise subprocess.CalledProcessError(1, "git")  # This fails, triggering fallback
            elif cmd == ["git", "rev-parse", "--abbrev-ref", "HEAD"]:
                return MagicMock(stdout="master\n", returncode=0)
            return MagicMock(stdout="", returncode=1)

        mock_run.side_effect = side_effect

        result = get_current_branch()

        assert result == "master"

    @patch("subprocess.run")
    def test_detached_head_fallback(self, mock_run):
        """Test detection when in detached HEAD state using fallback."""

        # Mock the sequence where sophisticated method fails and fallback returns HEAD
        def side_effect(cmd, **kwargs):
            if cmd == ["git", "rev-parse", "HEAD"]:
                return MagicMock(stdout="4cecea664abcd1234567890\n", returncode=0)
            elif cmd == ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"]:
                raise subprocess.CalledProcessError(1, "git")  # This fails, triggering fallback
            elif cmd == ["git", "rev-parse", "--abbrev-ref", "HEAD"]:
                return MagicMock(stdout="HEAD\n", returncode=0)
            return MagicMock(stdout="", returncode=1)

        mock_run.side_effect = side_effect

        result = get_current_branch()

        assert result == "master"  # fallback to master

    @patch("subprocess.run")
    def test_ci_branch_filtered_out_in_fallback(self, mock_run):
        """Test that CI auto-generated branches are filtered out in fallback."""

        # Mock the sequence where sophisticated method fails and fallback returns CI branch
        def side_effect(cmd, **kwargs):
            if cmd == ["git", "rev-parse", "HEAD"]:
                return MagicMock(stdout="4cecea664abcd1234567890\n", returncode=0)
            elif cmd == ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"]:
                raise subprocess.CalledProcessError(1, "git")  # This fails, triggering fallback
            elif cmd == ["git", "rev-parse", "--abbrev-ref", "HEAD"]:
                return MagicMock(stdout="evg-pr-test-12345\n", returncode=0)
            return MagicMock(stdout="", returncode=1)

        mock_run.side_effect = side_effect

        result = get_current_branch()

        assert result == "master"  # fallback to master when CI branch is detected

    @patch("subprocess.run")
    def test_git_command_fails(self, mock_run):
        """Test fallback when all git commands fail."""
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
