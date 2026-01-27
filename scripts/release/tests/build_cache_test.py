from unittest.mock import patch

from scripts.release.build_cache import build_cache_configuration, should_write_cache


class TestShouldWriteCache:
    """Test cache write decision logic for different Evergreen requester types.

    Cache write policy:
    - gitter_request (mainline commits): Write to master ✅
    - github_merge_request (merge queue): Write to master ✅
    - github_pull_request (PRs): Read-only ❌
    - patch_request (manual patches): Read-only ❌
    - No requester (local builds): Read-only ❌
    """

    @patch.dict("os.environ", {"requester": "gitter_request"}, clear=True)
    def test_mainline_commit_writes(self):
        """Mainline commits (gitter_request) should write to cache."""
        assert should_write_cache() is True

    @patch.dict("os.environ", {"requester": "github_merge_request"}, clear=True)
    def test_merge_queue_writes(self):
        """Merge queue builds (github_merge_request) should write to cache."""
        assert should_write_cache() is True

    @patch.dict("os.environ", {"requester": "github_pull_request"}, clear=True)
    def test_pr_does_not_write(self):
        """GitHub PRs (github_pull_request) should NOT write to cache."""
        assert should_write_cache() is False

    @patch.dict("os.environ", {"requester": "patch_request"}, clear=True)
    def test_manual_patch_does_not_write(self):
        """Manual patches (patch_request) should NOT write to cache."""
        assert should_write_cache() is False

    @patch.dict("os.environ", {"requester": "trigger_request"}, clear=True)
    def test_trigger_request_does_not_write(self):
        """Triggered builds (trigger_request) should NOT write to cache."""
        assert should_write_cache() is False

    @patch.dict("os.environ", {"requester": "ad_hoc"}, clear=True)
    def test_ad_hoc_does_not_write(self):
        """Ad-hoc builds (ad_hoc) should NOT write to cache."""
        assert should_write_cache() is False

    @patch.dict("os.environ", {}, clear=True)
    def test_no_requester_does_not_write(self):
        """Local builds (no requester env var) should NOT write to cache."""
        assert should_write_cache() is False


class TestBuildCacheConfiguration:
    """Test cache configuration building for different scenarios.

    All builds read from master cache.
    Only mainline merges and merge queue writes to master cache.
    """

    @patch.dict("os.environ", {"requester": "gitter_request"}, clear=True)
    def test_mainline_commit_reads_and_writes_master(self):
        """Mainline commits should read from master and write to master."""
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master only
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should write to master
        assert cache_to is not None
        assert cache_to["ref"] == f"{base_registry}:master"
        assert cache_to["mode"] == "max"
        assert cache_to["oci-mediatypes"] == "true"
        assert cache_to["image-manifest"] == "true"

    @patch.dict("os.environ", {"requester": "github_merge_request"}, clear=True)
    def test_merge_queue_reads_and_writes_master(self):
        """Merge queue builds should read from master and write to master."""
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master only
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should write to master
        assert cache_to is not None
        assert cache_to["ref"] == f"{base_registry}:master"

    @patch.dict("os.environ", {"requester": "github_pull_request"}, clear=True)
    def test_pr_reads_master_no_write(self):
        """GitHub PRs should read from master but NOT write."""
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should NOT write to cache
        assert cache_to is None

    @patch.dict("os.environ", {"requester": "patch_request"}, clear=True)
    def test_manual_patch_reads_master_no_write(self):
        """Manual patches should read from master but NOT write."""
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should NOT write to cache
        assert cache_to is None

    @patch.dict("os.environ", {}, clear=True)
    def test_local_build_reads_master_no_write(self):
        """Local builds (no requester) should read from master but NOT write."""
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes"
        cache_from, cache_to = build_cache_configuration(base_registry)

        # Should read from master
        expected_from = [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_from == expected_from

        # Should NOT write to cache
        assert cache_to is None
