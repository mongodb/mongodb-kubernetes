from git import Repo

from conftest import git_repo
from scripts.release.release_notes import generate_release_notes


def test_generate_release_notes_1_0_0(git_repo: Repo):
    assert False


def test_generate_release_notes_1_0_1(git_repo: Repo):
    repo_path = git_repo.working_dir
    git_repo.git.checkout("1.0.1")
    release_notes = generate_release_notes("1.0.0", repo_path)
    with open("scripts/release/testdata/release_notes_1.0.1.md") as file:
        assert release_notes == file.read()

def test_generate_release_notes_1_1_0(git_repo: Repo):
    repo_path = git_repo.working_dir
    git_repo.git.checkout("1.1.0")
    release_notes = generate_release_notes("1.0.1", repo_path)
    with open("scripts/release/testdata/release_notes_1.1.0.md") as file:
        assert release_notes == file.read()


def test_generate_release_notes_1_2_0(git_repo: Repo):
    repo_path = git_repo.working_dir
    git_repo.git.checkout("1.2.0")
    release_notes = generate_release_notes("1.1.0", repo_path)
    with open("scripts/release/testdata/release_notes_1.2.0.md") as file:
        assert release_notes == file.read()
