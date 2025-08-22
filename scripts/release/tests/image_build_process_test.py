from unittest.mock import patch, MagicMock

import pytest

from scripts.release.build.image_build_process import (
    build_cache_configuration,
    ensure_all_cache_repositories,
)


class TestBuildCacheConfiguration:
    """Test cache configuration building for different scenarios."""

    @patch('scripts.release.build.image_build_process.get_cache_scope')
    @patch('scripts.release.build.image_build_process.get_current_branch')
    def test_single_platform_master_branch(self, mock_branch, mock_scope):
        """Test cache configuration for single platform build on master branch."""
        mock_branch.return_value = "master"
        mock_scope.return_value = "master"

        cache_from, cache_to, repos = build_cache_configuration(
            "mongodb-kubernetes"
        )

        # Should read from master-arch and shared-arch
        expected_from = [
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-amd64",
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:shared-linux-amd64"
        ]
        assert cache_from == expected_from

        # Should write to master-arch
        assert len(cache_to) == 1
        assert cache_to[0]["ref"] == "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-amd64"
        assert cache_to[0]["mode"] == "max"
        assert cache_to[0]["oci-mediatypes"] == "true"
        assert cache_to[0]["image-manifest"] == "true"

        # Should ensure one repository
        assert repos == {"dev/cache/mongodb-kubernetes"}

    @patch('scripts.release.build.image_build_process.get_cache_scope')
    @patch('scripts.release.build.image_build_process.get_current_branch')
    def test_single_platform_feature_branch(self, mock_branch, mock_scope):
        """Test cache configuration for single platform build on feature branch."""
        mock_branch.return_value = "feature/new-cache"
        mock_scope.return_value = "feature-new-cache"

        cache_from, cache_to, repos = build_cache_configuration(
            "mongodb-kubernetes"
        )

        # Should read from branch-arch, master-arch, and shared-arch
        expected_from = [
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:feature-new-cache-linux-amd64",
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-amd64",
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:shared-linux-amd64"
        ]
        assert cache_from == expected_from

        # Should write to branch-arch only (not master since we're not on master)
        assert len(cache_to) == 1
        assert cache_to[0]["ref"] == "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:feature-new-cache-linux-amd64"

    @patch('scripts.release.build.image_build_process.get_cache_scope')
    @patch('scripts.release.build.image_build_process.get_current_branch')
    def test_multi_platform_master_branch(self, mock_branch, mock_scope):
        """Test cache configuration for multi-platform build on master branch."""
        mock_branch.return_value = "master"
        mock_scope.return_value = "master"

        cache_from, cache_to, repos = build_cache_configuration(
            "mongodb-kubernetes"
        )

        # Should have cache entries for both architectures
        assert len(cache_from) == 4  # 2 platforms * 2 refs each (master-arch + shared-arch)
        assert len(cache_to) == 2    # 2 platforms * 1 ref each (master-arch)

        # Check amd64 cache from
        assert "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-amd64" in cache_from
        assert "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:shared-linux-amd64" in cache_from

        # Check arm64 cache from
        assert "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-arm64" in cache_from
        assert "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:shared-linux-arm64" in cache_from

        # Check cache to targets
        cache_to_refs = [entry["ref"] for entry in cache_to]
        assert "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-amd64" in cache_to_refs
        assert "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-arm64" in cache_to_refs

    @patch('scripts.release.build.image_build_process.get_cache_scope')
    @patch('scripts.release.build.image_build_process.get_current_branch')
    def test_multi_platform_feature_branch(self, mock_branch, mock_scope):
        """Test cache configuration for multi-platform build on feature branch."""
        mock_branch.return_value = "feature/new-cache"
        mock_scope.return_value = "feature-new-cache"

        cache_from, cache_to, repos = build_cache_configuration(
            "mongodb-kubernetes"
        )

        # Should have cache entries for both architectures with 3 precedence levels each
        assert len(cache_from) == 6  # 2 platforms * 3 refs each (branch-arch + master-arch + shared-arch)
        assert len(cache_to) == 2    # 2 platforms * 1 ref each (branch-arch only)

        # Check amd64 cache from precedence
        expected_amd64_from = [
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:feature-new-cache-linux-amd64",
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:master-linux-amd64",
            "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:shared-linux-amd64"
        ]
        for ref in expected_amd64_from:
            assert ref in cache_from

        # Check cache to targets (should only write to branch-specific cache)
        cache_to_refs = [entry["ref"] for entry in cache_to]
        assert "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:feature-new-cache-linux-amd64" in cache_to_refs
        assert "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:feature-new-cache-linux-arm64" in cache_to_refs

        # Should not write to master cache when not on master branch
        master_refs = [ref for ref in cache_to_refs if ":master-" in ref]
        assert len(master_refs) == 0

    @patch('scripts.release.build.image_build_process.get_cache_scope')
    @patch('scripts.release.build.image_build_process.get_current_branch')
    def test_patch_build_with_version_id(self, mock_branch, mock_scope):
        """Test cache configuration for patch build with version ID."""
        mock_branch.return_value = "feature/new-cache"
        mock_scope.return_value = "feature-new-cache-6899b7e3"

        cache_from, cache_to, repos = build_cache_configuration(
            "mongodb-kubernetes"
        )

        # Should include version ID in cache refs
        assert "type=registry,ref=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:feature-new-cache-6899b7e3-linux-amd64" in cache_from
        assert cache_to[0]["ref"] == "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes:feature-new-cache-6899b7e3-linux-amd64"

    def test_custom_account_and_region(self):
        """Test cache configuration with custom AWS account and region."""
        with patch('scripts.release.build.image_build_process.get_cache_scope') as mock_scope, \
             patch('scripts.release.build.image_build_process.get_current_branch') as mock_branch:

            mock_branch.return_value = "master"
            mock_scope.return_value = "master"

            cache_from, cache_to, repos = build_cache_configuration(
                "test-image", account_id="123456789012", region="us-west-2"
            )

            # Should use custom account and region
            expected_registry = "123456789012.dkr.ecr.us-west-2.amazonaws.com/dev/cache/test-image"
            assert cache_from[0].startswith(f"type=registry,ref={expected_registry}")
            assert cache_to[0]["ref"].startswith(expected_registry)

    def test_empty_platforms_fallback(self):
        """Test cache configuration with empty platforms list."""
        with patch('scripts.release.build.image_build_process.get_cache_scope') as mock_scope, \
             patch('scripts.release.build.image_build_process.get_current_branch') as mock_branch:

            mock_branch.return_value = "master"
            mock_scope.return_value = "master"

            cache_from, cache_to, repos = build_cache_configuration(
                "test-image"
            )

            # Should fallback to linux-amd64
            assert "linux-amd64" in cache_from[0]
            assert "linux-amd64" in cache_to[0]["ref"]


class TestEnsureAllCacheRepositories:
    """Test cache repository management."""

    @patch('scripts.release.build.image_build_process.ensure_ecr_cache_repository')
    def test_ensure_multiple_repositories(self, mock_ensure):
        """Test ensuring multiple cache repositories."""
        image_names = ["mongodb-kubernetes", "init-database", "init-ops-manager"]

        ensure_all_cache_repositories(image_names)

        # Should call ensure_ecr_cache_repository for each image
        assert mock_ensure.call_count == 3
        expected_calls = [
            "dev/cache/mongodb-kubernetes",
            "dev/cache/init-database",
            "dev/cache/init-ops-manager"
        ]
        actual_calls = [call[0][0] for call in mock_ensure.call_args_list]
        assert actual_calls == expected_calls

    @patch('scripts.release.build.image_build_process.ensure_ecr_cache_repository')
    def test_ensure_with_custom_region(self, mock_ensure):
        """Test ensuring repositories with custom region."""
        image_names = ["test-image"]

        ensure_all_cache_repositories(image_names, region="eu-west-1")

        mock_ensure.assert_called_once_with("dev/cache/test-image", "eu-west-1")

    @patch('scripts.release.build.image_build_process.ensure_ecr_cache_repository')
    def test_ensure_empty_list(self, mock_ensure):
        """Test ensuring repositories with empty image list."""
        ensure_all_cache_repositories([])

        mock_ensure.assert_not_called()
