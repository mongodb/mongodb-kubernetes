import os
import unittest

from git import Repo

from scripts.release.changelog import DEFAULT_INITIAL_GIT_TAG_VERSION, ChangeKind
from scripts.release.version import (
    Environment,
    get_version_for_environment,
    increment_previous_version,
)


class TestGetVersionForEnvironment:

    def test_dev_environment(self, git_repo: Repo):
        os.environ["BUILD_ID"] = "688364423f9b6c00072b3556"
        expected_version = os.environ["BUILD_ID"]

        version = get_version_for_environment(
            env=Environment.DEV,
            initial_commit_sha=None,
            initial_version=DEFAULT_INITIAL_GIT_TAG_VERSION,
            repository_path=git_repo.working_dir,
        )

        assert version == expected_version

    def test_staging_environment(self, git_repo: Repo):
        initial_commit = list(git_repo.iter_commits(reverse=True))[4]
        git_repo.git.checkout(initial_commit)
        expected_version = initial_commit.hexsha[:8]

        version = get_version_for_environment(
            env=Environment.STAGING,
            initial_commit_sha=None,
            initial_version=DEFAULT_INITIAL_GIT_TAG_VERSION,
            repository_path=git_repo.working_dir,
        )

        assert version == expected_version

    def test_prod_environment(self, git_repo: Repo):
        git_repo.git.checkout("1.2.0")

        version = get_version_for_environment(
            env=Environment.PROD,
            initial_commit_sha=None,
            initial_version=DEFAULT_INITIAL_GIT_TAG_VERSION,
            repository_path=git_repo.working_dir,
        )

        assert version == "1.2.0"


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
