from scripts.release.changelog_test import create_git_repo
from scripts.release.release_notes import generate_release_notes


def test_generate_release_notes():
    repo, repo_path = create_git_repo()
    release_notes = generate_release_notes("1.0.0", repo_path)
    assert release_notes is not None
