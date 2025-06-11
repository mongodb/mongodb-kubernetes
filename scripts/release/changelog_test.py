from git import Repo

import changelog
from conftest import git_repo
from scripts.release.changelog import CHANGELOG_PATH


def test_get_changelog_entries(git_repo: Repo):
    repo_path = git_repo.working_dir
    entries = changelog.get_changelog_entries("1.0.0", repo_path, CHANGELOG_PATH)
