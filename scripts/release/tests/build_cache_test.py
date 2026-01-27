from unittest.mock import patch

from scripts.release.build_cache import build_cache_configuration, get_cache_scope, get_current_branch


class TestGetCurrentBranch:
    """Test branch detection logic for different scenarios.

    The implementation uses Evergreen environment variables:
    - github_pr_head_branch: For PR builds, provides the PR's source branch
    - branch_name: For mainline/release builds, provides the project's tracked branch
    """

    @patch.dict("os.environ", {"github_pr_head_branch": "fork-feature-branch"}, clear=True)
    def test_github_pr_head_branch_env_var(self):
        """Test that github_pr_head_branch env var takes precedence (for PR builds)."""
        result, should_write = get_current_branch()

        assert result == "fork-feature-branch"
        assert should_write is True

    @patch.dict("os.environ", {"github_pr_head_branch": "feature/my-branch", "branch_name": "master"}, clear=True)
    def test_github_pr_head_branch_takes_precedence(self):
        """Test that github_pr_head_branch takes precedence over branch_name."""
        result, should_write = get_current_branch()

        assert result == "feature/my-branch"
        assert should_write is True

    @patch.dict("os.environ", {"branch_name": "master"}, clear=True)
    def test_branch_name_env_var(self):
        """Test that branch_name env var is used for mainline builds."""
        result, should_write = get_current_branch()

        assert result == "master"
        assert should_write is True

    @patch.dict("os.environ", {"branch_name": "release-1.0"}, clear=True)
    def test_branch_name_for_release_branch(self):
        """Test branch_name for release branch builds."""
        result, should_write = get_current_branch()

        assert result == "release-1.0"
        assert should_write is True

    @patch.dict("os.environ", {}, clear=True)
    def test_fallback_when_no_env_vars(self):
        """Test fallback to master when no env vars are set (e.g., local builds)."""
        result, should_write = get_current_branch()

        assert result == "master"
        assert should_write is False  # Fallback means no cache write


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
