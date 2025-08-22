from unittest.mock import patch

from scripts.release.build.image_build_process import (
    build_cache_configuration,
)


class TestBuildCacheConfiguration:
    """Test cache configuration building for different scenarios."""

    @patch("scripts.release.build.image_build_process.get_cache_scope")
    @patch("scripts.release.build.image_build_process.get_current_branch")
    def test_master_branch(self, mock_branch, mock_scope):
        """Test cache configuration for master branch."""
        mock_branch.return_value = "master"
        mock_scope.return_value = "master"

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master and shared
        expected_from = [
            {"type": "registry", "ref": f"{base_registry}:master"},
            {"type": "registry", "ref": f"{base_registry}:shared"},
        ]
        assert cache_from == expected_from

        # Should write to master
        assert len(cache_to) == 1
        assert cache_to[0]["ref"] == f"{base_registry}:master"
        assert cache_to[0]["mode"] == "max"
        assert cache_to[0]["oci-mediatypes"] == "true"
        assert cache_to[0]["image-manifest"] == "true"

    @patch("scripts.release.build.image_build_process.get_cache_scope")
    @patch("scripts.release.build.image_build_process.get_current_branch")
    def test_feature_branch(self, mock_branch, mock_scope):
        """Test cache configuration for feature branch."""
        mock_branch.return_value = "feature/new-cache"
        mock_scope.return_value = "feature-new-cache"

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from branch, master, and shared
        expected_from = [
            {"type": "registry", "ref": f"{base_registry}:feature-new-cache"},
            {"type": "registry", "ref": f"{base_registry}:master"},
            {"type": "registry", "ref": f"{base_registry}:shared"},
        ]
        assert cache_from == expected_from

        # Should write to branch only (not master since we're not on master)
        assert len(cache_to) == 1
        assert cache_to[0]["ref"] == f"{base_registry}:feature-new-cache"
        assert cache_to[0]["mode"] == "max"
        assert cache_to[0]["oci-mediatypes"] == "true"
        assert cache_to[0]["image-manifest"] == "true"

    @patch("scripts.release.build.image_build_process.get_cache_scope")
    @patch("scripts.release.build.image_build_process.get_current_branch")
    def test_patch_build_with_version_id(self, mock_branch, mock_scope):
        """Test cache configuration for patch build with version ID."""
        mock_branch.return_value = "feature/new-cache"
        mock_scope.return_value = "feature-new-cache-6899b7e3"

        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should include version ID in cache refs
        assert cache_from[0]["ref"] == f"{base_registry}:feature-new-cache-6899b7e3"
        assert cache_to[0]["ref"] == f"{base_registry}:feature-new-cache-6899b7e3"
