from unittest.mock import patch

from scripts.release.build.image_build_process import (
    build_cache_configuration,
)


class TestBuildCacheConfiguration:
    """Test cache configuration building for different scenarios."""

    @patch("scripts.release.build.image_build_process.get_cache_scope")
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

    @patch("scripts.release.build.image_build_process.get_cache_scope")
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

    @patch("scripts.release.build.image_build_process.get_cache_scope")
    def test_sanitized_branch_name(self, mock_scope):
        """Test cache configuration with sanitized branch name."""
        mock_scope.return_value = "feature-new-cache-123"

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should include sanitized branch name in cache refs
        assert cache_from[0]["ref"] == f"{base_registry}:feature-new-cache-123"
        assert cache_to["ref"] == f"{base_registry}:feature-new-cache-123"
