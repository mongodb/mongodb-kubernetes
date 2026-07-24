from unittest.mock import patch

from scripts.release.build_cache import build_cache_configuration, should_write_cache


class TestShouldWriteCache:
    """Test cache write decision: only gitter_request writes to master."""

    @patch.dict("os.environ", {"requester": "gitter_request"}, clear=True)
    def test_gitter_request_writes(self):
        assert should_write_cache() is True

    @patch.dict("os.environ", {"requester": "github_pull_request"}, clear=True)
    def test_other_requesters_do_not_write(self):
        assert should_write_cache() is False


class TestBuildCacheConfiguration:
    """Test cache config: read/write the current branch's own cache, only gitter_request writes."""

    @patch.dict("os.environ", {"requester": "gitter_request"}, clear=True)
    def test_gitter_request_reads_and_writes_master_by_default(self):
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/test"
        cache_from, cache_to = build_cache_configuration(base_registry)

        assert cache_from == [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_to["ref"] == f"{base_registry}:master"
        assert cache_to["mode"] == "max"

    @patch.dict("os.environ", {"requester": "github_pull_request"}, clear=True)
    def test_other_requesters_read_only(self):
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/test"
        cache_from, cache_to = build_cache_configuration(base_registry)

        assert cache_from == [{"type": "registry", "ref": f"{base_registry}:master"}]
        assert cache_to is None

    @patch.dict("os.environ", {"requester": "gitter_request", "branch_name": "v1"}, clear=True)
    def test_backport_branch_reads_and_writes_its_own_cache(self):
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/test"
        cache_from, cache_to = build_cache_configuration(base_registry)

        assert cache_from == [{"type": "registry", "ref": f"{base_registry}:v1"}]
        assert cache_to["ref"] == f"{base_registry}:v1"

    @patch.dict("os.environ", {"requester": "github_pull_request", "branch_name": "v2"}, clear=True)
    def test_backport_branch_pr_does_not_pollute_master_cache(self):
        base_registry = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/test"
        cache_from, cache_to = build_cache_configuration(base_registry)

        assert cache_from == [{"type": "registry", "ref": f"{base_registry}:v2"}]
        assert cache_to is None
