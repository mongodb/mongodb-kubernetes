from scripts.release.pipeline import branch_latest_tag_for


class TestBranchLatestTagFor:
    def test_master_has_no_branch_latest_tag(self):
        assert branch_latest_tag_for("master") is None

    def test_v1_branch(self):
        assert branch_latest_tag_for("v1") == "latest-v1"

    def test_v2_branch(self):
        assert branch_latest_tag_for("v2") == "latest-v2"

    def test_untracked_branch(self):
        assert branch_latest_tag_for("some-feature-branch") is None

    def test_none_branch(self):
        assert branch_latest_tag_for(None) is None
