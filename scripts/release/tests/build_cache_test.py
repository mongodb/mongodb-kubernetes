import subprocess
from unittest.mock import MagicMock, patch

from scripts.release.build_cache import build_cache_configuration, get_cache_scope, get_current_branch


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

    @patch("scripts.release.build_cache.get_current_branch")
    def test_feature_branch(self, mock_branch):
        """Test cache scope for feature branch."""
        mock_branch.return_value = "feature/new-cache"

        result = get_cache_scope()

        assert result == "feature-new-cache"

    @patch("scripts.release.build_cache.get_current_branch")
    def test_branch_name_sanitization(self, mock_branch):
        """Test branch name sanitization for cache scope."""
        mock_branch.return_value = "Feature/NEW_cache@123"

        result = get_cache_scope()

        assert result == "feature-new_cache-123"

    @patch("scripts.release.build_cache.get_current_branch")
    def test_complex_branch_name(self, mock_branch):
        """Test cache scope for complex branch name with special characters."""
        mock_branch.return_value = "user/feature-123_test.branch"

        result = get_cache_scope()

        assert result == "user-feature-123_test.branch"


class TestBuildCacheConfiguration:
    """Test cache configuration building for different scenarios."""

    @patch("scripts.release.build_cache.get_cache_scope")
    def test_master_branch(self, mock_scope):
        """Test cache configuration for master branch."""
        mock_scope.return_value = "master"

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master only
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should write to master
        assert cache_to["ref"] == f"{base_registry}:master"
        assert cache_to["mode"] == "max"
        assert cache_to["oci-mediatypes"] == "true"
        assert cache_to["image-manifest"] == "true"

    @patch("scripts.release.build_cache.get_cache_scope")
    def test_feature_branch(self, mock_scope):
        """Test cache configuration for feature branch."""
        mock_scope.return_value = "feature-new-cache"

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from branch and master
        expected_from = [
            {"type": "registry", "ref": f"{base_registry}:feature-new-cache"},
            {"type": "registry", "ref": f"{base_registry}:master"},
        ]
        assert cache_from == expected_from

        # Should write to branch only
        assert cache_to["ref"] == f"{base_registry}:feature-new-cache"
        assert cache_to["mode"] == "max"
        assert cache_to["oci-mediatypes"] == "true"
        assert cache_to["image-manifest"] == "true"

    @patch("scripts.release.build_cache.get_cache_scope")
    def test_sanitized_branch_name(self, mock_scope):
        """Test cache configuration with sanitized branch name."""
        mock_scope.return_value = "feature-new-cache-123"

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should include sanitized branch name in cache refs
        assert cache_from[0]["ref"] == f"{base_registry}:feature-new-cache-123"
        assert cache_to["ref"] == f"{base_registry}:feature-new-cache-123"
