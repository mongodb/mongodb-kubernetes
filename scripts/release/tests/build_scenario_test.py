import os

from git import Repo

from scripts.release.build.build_scenario import BuildScenario
from scripts.release.constants import DEFAULT_CHANGELOG_PATH


class TestGetVersionForBuildScenario:

    def test_patch_build_scenario(self, git_repo: Repo):
        os.environ["BUILD_ID"] = "688364423f9b6c00072b3556"
        expected_version = os.environ["BUILD_ID"]

        version = BuildScenario.PATCH.get_version(
            repository_path=git_repo.working_dir, changelog_sub_path=DEFAULT_CHANGELOG_PATH
        )

        assert version == expected_version

    def test_staging_build_scenario(self, git_repo: Repo):
        initial_commit = list(git_repo.iter_commits(reverse=True))[4]
        git_repo.git.checkout(initial_commit)
        expected_version = initial_commit.hexsha[:8]

        version = BuildScenario.STAGING.get_version(
            repository_path=git_repo.working_dir, changelog_sub_path=DEFAULT_CHANGELOG_PATH
        )

        assert version == expected_version

    def test_release_build_scenario(self, git_repo: Repo):
        git_repo.git.checkout("1.2.0")

        version = BuildScenario.RELEASE.get_version(
            repository_path=git_repo.working_dir, changelog_sub_path=DEFAULT_CHANGELOG_PATH
        )

        assert version == "1.2.0"
