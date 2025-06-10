import os
import shutil

import changelog
from git import Repo
import tempfile

from scripts.release.changelog import CHANGELOG_PATH


def create_git_repo():
    """Create a temporary git repository for testing."""

    repo_dir = tempfile.mkdtemp()
    repo = Repo.init(repo_dir)

    ## First commit
    file_name = create_new_file(repo_dir, "new-file.txt", "Initial content\n")
    repo.index.add([file_name])
    repo.index.commit("initial commit")
    repo.create_tag("1.0.0", message="Initial release")

    ## Second commit
    file_name = create_new_file(repo_dir, "another-file.txt", "Added more content\n")
    repo.index.add([file_name])
    repo.index.commit("additional changes")

    changelog_path = os.path.join(repo_dir, CHANGELOG_PATH)
    os.mkdir(changelog_path)
    file_name = add_file(repo_dir, "changelog/20250610_feature_oidc.md")
    repo.index.add([file_name])

    return repo, repo_dir

def create_new_file(repo_path: str, file_path: str, file_content: str):
    """Create a new file in the repository."""

    file_name = os.path.join(repo_path, file_path)
    with open(file_name, "a") as f:
        f.write(file_content)

    return file_name

def add_file(repo_path: str, file_path: str):
    """Adds a file in the repository path."""

    dst_path = os.path.join(repo_path, file_path)
    src_path = os.path.join('scripts/release/testdata', file_path)

    return shutil.copy(src_path, dst_path)

def test_get_changelog_entries():
    repo, repo_path = create_git_repo()
    entries = changelog.get_changelog_entries("1.0.0", repo_path, CHANGELOG_PATH)
