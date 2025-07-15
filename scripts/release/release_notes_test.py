from git import Repo

from scripts.release.conftest import git_repo
from scripts.release.release_notes import generate_release_notes


def test_generate_release_notes_before_1_0_0(git_repo: Repo):
    initial_commit = list(git_repo.iter_commits(reverse=True))[0]
    git_repo.git.checkout(initial_commit)
    release_notes = generate_release_notes(git_repo.working_dir)
    with open("scripts/release/testdata/release_notes_1.0.0_empty.md") as file:
        assert release_notes == file.read()


def test_generate_release_notes_1_0_0(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.0.0")


def test_generate_release_notes_1_0_1(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.0.1")


def test_generate_release_notes_1_1_0(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.1.0")


def test_generate_release_notes_1_2_0(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.2.0")


def test_generate_release_notes_2_0_0(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "2.0.0")


def test_generate_release_notes_1_2_1(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.2.1")


def test_generate_release_notes_2_0_1(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "2.0.1")


def test_generate_release_notes_1_2_2(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.2.2")


def test_generate_release_notes_2_0_2(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "2.0.2")


def test_generate_release_notes_1_2_3(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.2.3")


def test_generate_release_notes_3_0_0(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "3.0.0")


def test_generate_release_notes_2_0_3(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "2.0.3")


def test_generate_release_notes_1_2_4(git_repo: Repo):
    checkout_and_assert_release_notes(git_repo, "1.2.4")


def checkout_and_assert_release_notes(git_repo: Repo, tag: str):
    git_repo.git.checkout(tag)
    release_notes = generate_release_notes(git_repo.working_dir)
    with open(f"scripts/release/testdata/release_notes_{tag}.md") as file:
        assert release_notes == file.read()
