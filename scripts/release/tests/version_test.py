import unittest

import pytest
from git import Repo

from scripts.release.changelog import ChangeKind
from scripts.release.version import find_previous_version_tag, increment_previous_version


class TestCalculateNextReleaseVersion(unittest.TestCase):

    def test_bump_major_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.BREAKING]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_bump_minor_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_bump_patch_version(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FIX]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_bump_patch_version_other_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_bump_patch_version_no_changes(self):
        previous_version = "1.2.3"
        changelog = []
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_feature_takes_precedence(self):
        # Test that FEATURE has precedence over FIX
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE, ChangeKind.FIX]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_breaking_takes_precedence(self):
        # Test that BREAKING has precedence over FEATURE and FIX
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE, ChangeKind.BREAKING, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_multiple_breaking_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.BREAKING, ChangeKind.BREAKING, ChangeKind.FEATURE, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "2.0.0")

    def test_multiple_feature_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FEATURE, ChangeKind.FEATURE, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.3.0")

    def test_multiple_fix_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.FIX, ChangeKind.FIX, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")

    def test_multiple_other_changes(self):
        previous_version = "1.2.3"
        changelog = [ChangeKind.OTHER, ChangeKind.OTHER]
        next_version = increment_previous_version(previous_version, changelog)
        self.assertEqual(next_version, "1.2.4")


@pytest.fixture
def repo_with_remote(tmp_path):
    """
    Linear history: commit_a (1.0.0) → commit_b (1.0.1) → commit_c (HEAD, untagged).
    Remote is a bare clone; local is cloned from it with all tags initially in sync.
    """
    source_dir = tmp_path / "source"
    source_dir.mkdir()
    source = Repo.init(str(source_dir))
    source.git.checkout("-b", "master")

    (source_dir / "a.txt").write_text("a")
    source.index.add(["a.txt"])
    source.index.commit("commit a")
    source.create_tag("1.0.0")

    (source_dir / "b.txt").write_text("b")
    source.index.add(["b.txt"])
    source.index.commit("commit b")
    source.create_tag("1.0.1")

    (source_dir / "c.txt").write_text("c")
    source.index.add(["c.txt"])
    source.index.commit("commit c")

    remote_dir = tmp_path / "remote.git"
    Repo.clone_from(str(source_dir), str(remote_dir), bare=True)
    local = Repo.clone_from(str(remote_dir), str(tmp_path / "local"))
    return local, remote_dir, tmp_path


def test_returns_latest_ancestor_tag(repo_with_remote):
    local, _, _ = repo_with_remote
    result = find_previous_version_tag(local)
    assert result is not None
    assert result.name == "1.0.1"


def test_finds_remote_tag_not_locally_fetched(repo_with_remote):
    """A tag present on remote but absent locally must still be returned after fetch."""
    local, _, _ = repo_with_remote
    local.delete_tag(local.tags["1.0.1"])  # simulate tag not yet fetched

    result = find_previous_version_tag(local)
    assert result is not None
    assert result.name == "1.0.1"


def test_ignores_local_only_tag(repo_with_remote):
    """A tag created locally but never pushed to remote must not be returned."""
    local, _, _ = repo_with_remote
    local.create_tag("1.0.2")  # local-only, higher than anything on remote

    result = find_previous_version_tag(local)
    assert result is not None
    assert result.name == "1.0.1"


def test_ignores_tags_from_unrelated_remote(repo_with_remote, tmp_path):
    """Tags fetched from a second remote (e.g. MEKO) must not be returned."""
    local, remote_dir, tmp_path = repo_with_remote

    # Set up a second remote with its own tag history
    other_remote_dir = tmp_path / "other_remote.git"
    other_source_dir = tmp_path / "other_source"
    other_source_dir.mkdir()
    other_source = Repo.init(str(other_source_dir))
    other_source.git.checkout("-b", "master")
    (other_source_dir / "x.txt").write_text("x")
    other_source.index.add(["x.txt"])
    other_source.index.commit("other repo initial commit")
    other_source.create_tag("5.0.0")  # higher version than anything on origin
    Repo.clone_from(str(other_source_dir), str(other_remote_dir), bare=True)

    local.create_remote("other", str(other_remote_dir))
    local.git.fetch("other", "--tags")  # pulls 5.0.0 into local tag namespace

    result = find_previous_version_tag(local)
    assert result is not None
    assert result.name == "1.0.1"  # not 5.0.0 from the other remote


def test_returns_none_when_remote_has_no_tags(tmp_path):
    source_dir = tmp_path / "source"
    source_dir.mkdir()
    source = Repo.init(str(source_dir))
    source.git.checkout("-b", "master")
    (source_dir / "a.txt").write_text("a")
    source.index.add(["a.txt"])
    source.index.commit("commit a")

    remote_dir = tmp_path / "remote.git"
    Repo.clone_from(str(source_dir), str(remote_dir), bare=True)
    local = Repo.clone_from(str(remote_dir), str(tmp_path / "local"))

    assert find_previous_version_tag(local) is None


def test_returns_none_when_no_origin_remote(tmp_path):
    repo = Repo.init(str(tmp_path / "local"))
    repo.git.checkout("-b", "master")
    (tmp_path / "local" / "a.txt").write_text("a")
    repo.index.add(["a.txt"])
    repo.index.commit("commit a")
    repo.create_tag("1.0.0")

    assert find_previous_version_tag(repo) is None
