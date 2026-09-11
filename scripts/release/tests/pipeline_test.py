from scripts.release.pipeline import branch_latest_tag_for


class TestBranchLatestTagFor:
    def test_master_has_no_branch_latest_tag(self):
        assert branch_latest_tag_for("master") is None

    def test_v1_branch(self):
        assert branch_latest_tag_for("release-v1") == "latest-release-v1"

    def test_v2_branch(self):
        assert branch_latest_tag_for("release-v2") == "latest-release-v2"

    def test_untracked_branch(self):
        assert branch_latest_tag_for("some-feature-branch") is None

    def test_none_branch(self):
        assert branch_latest_tag_for(None) is None

    def test_v1_test_branch(self):
        assert branch_latest_tag_for("release-v1-test") == "latest-release-v1-test"

    def test_v2_test_branch(self):
        assert branch_latest_tag_for("release-v2-test") == "latest-release-v2-test"

    def test_v_test_no_number(self):
        assert branch_latest_tag_for("v-test") is None

    def test_missing_v_prefix(self):
        assert branch_latest_tag_for("1-test") is None
