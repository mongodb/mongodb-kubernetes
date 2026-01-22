import subprocess
from unittest.mock import MagicMock, patch

from scripts.release.build_cache import build_cache_configuration, get_cache_scope, get_current_branch


class TestGetCurrentBranch:
    """Test branch detection logic for different scenarios."""

    @patch.dict("os.environ", {"github_pr_head_branch": "fork-feature-branch"})
    @patch("subprocess.run")
    def test_github_pr_head_branch_env_var(self, mock_run):
        """Test that github_pr_head_branch env var takes precedence (for fork PRs)."""
        result, should_write = get_current_branch()

        assert result == "fork-feature-branch"
        assert should_write is True
        # Git commands should not be called when env var is set
        mock_run.assert_not_called()

    @patch.dict("os.environ", {}, clear=True)
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

        result, should_write = get_current_branch()

        assert result == "add-caching"
        assert should_write is True

    @patch.dict("os.environ", {}, clear=True)
    @patch("subprocess.run")
    def test_master_branch_fallback(self, mock_run):
        """Test fallback to master when git commands fail."""

        # Mock the sequence where git commands fail, triggering fallback
        def side_effect(cmd, **kwargs):
            if cmd == ["git", "rev-parse", "HEAD"]:
                return MagicMock(stdout="4cecea664abcd1234567890\n", returncode=0)
            elif cmd == ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"]:
                raise subprocess.CalledProcessError(1, "git")  # This fails, triggering fallback
            return MagicMock(stdout="", returncode=1)

        mock_run.side_effect = side_effect

        result, should_write = get_current_branch()

        assert result == "master"
        assert should_write is False  # Fallback means not positively detected

    @patch.dict("os.environ", {}, clear=True)
    @patch("subprocess.run")
    def test_no_matching_branch_fallback(self, mock_run):
        """Test fallback when no matching branch is found."""

        # Mock the sequence where no branch matches the current commit
        def side_effect(cmd, **kwargs):
            if cmd == ["git", "rev-parse", "HEAD"]:
                return MagicMock(stdout="4cecea664abcd1234567890\n", returncode=0)
            elif cmd == ["git", "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/remotes/origin"]:
                return MagicMock(
                    stdout="origin/master 1234567890abcdef\norigin/other-branch 9999999999999999\n",
                    returncode=0,
                )
            return MagicMock(stdout="", returncode=1)

        mock_run.side_effect = side_effect

        result, should_write = get_current_branch()

        assert result == "master"
        assert should_write is False  # No match means fallback

    @patch.dict("os.environ", {}, clear=True)
    @patch("subprocess.run")
    def test_git_command_fails(self, mock_run):
        """Test fallback when all git commands fail."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result, should_write = get_current_branch()

        assert result == "master"  # fallback to master
        assert should_write is False

    @patch.dict("os.environ", {}, clear=True)
    @patch("subprocess.run")
    def test_git_not_found(self, mock_run):
        """Test fallback when git is not found."""
        mock_run.side_effect = FileNotFoundError("git not found")

        result, should_write = get_current_branch()

        assert result == "master"  # fallback to master
        assert should_write is False


class TestGetCacheScope:
    """Test cache scope generation for different scenarios."""

    @patch("scripts.release.build_cache.get_current_branch")
    def test_feature_branch(self, mock_branch):
        """Test cache scope for feature branch."""
        mock_branch.return_value = ("feature/new-cache", True)

        result, should_write = get_cache_scope()

        assert result == "feature-new-cache"
        assert should_write is True

    @patch("scripts.release.build_cache.get_current_branch")
    def test_branch_name_sanitization(self, mock_branch):
        """Test branch name sanitization for cache scope."""
        mock_branch.return_value = ("Feature/NEW_cache@123", True)

        result, should_write = get_cache_scope()

        assert result == "feature-new_cache-123"
        assert should_write is True

    @patch("scripts.release.build_cache.get_current_branch")
    def test_complex_branch_name(self, mock_branch):
        """Test cache scope for complex branch name with special characters."""
        mock_branch.return_value = ("user/feature-123_test.branch", True)

        result, should_write = get_cache_scope()

        assert result == "user-feature-123_test.branch"
        assert should_write is True

    @patch("scripts.release.build_cache.get_current_branch")
    def test_fallback_branch_no_write(self, mock_branch):
        """Test that fallback branches should not write to cache."""
        mock_branch.return_value = ("master", False)

        result, should_write = get_cache_scope()

        assert result == "master"
        assert should_write is False


class TestBuildCacheConfiguration:
    """Test cache configuration building for different scenarios."""

    @patch("scripts.release.build_cache.get_cache_scope")
    def test_master_branch_detected(self, mock_scope):
        """Test cache configuration for positively detected master branch."""
        mock_scope.return_value = ("master", True)

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master only
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should write to master (positively detected)
        assert cache_to["ref"] == f"{base_registry}:master"
        assert cache_to["mode"] == "max"
        assert cache_to["oci-mediatypes"] == "true"
        assert cache_to["image-manifest"] == "true"

    @patch("scripts.release.build_cache.get_cache_scope")
    def test_master_branch_fallback_no_write(self, mock_scope):
        """Test cache configuration when falling back to master (no cache write)."""
        mock_scope.return_value = ("master", False)

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should NOT write to cache (fallback)
        assert cache_to is None

    @patch("scripts.release.build_cache.get_cache_scope")
    def test_feature_branch(self, mock_scope):
        """Test cache configuration for feature branch."""
        mock_scope.return_value = ("feature-new-cache", True)

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
        mock_scope.return_value = ("feature-new-cache-123", True)

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should include sanitized branch name in cache refs
        assert cache_from[0]["ref"] == f"{base_registry}:feature-new-cache-123"
        assert cache_to["ref"] == f"{base_registry}:feature-new-cache-123"
