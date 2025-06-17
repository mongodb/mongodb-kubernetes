import argparse
import pathlib

from git import Repo

from scripts.release.changelog import CHANGELOG_PATH
from scripts.release.release_notes import calculate_next_version_with_changelog


def next_release_version(
    repository_path: str = ".",
    changelog_sub_path: str = CHANGELOG_PATH,
    initial_commit_sha: str = None,
    initial_version: str = "1.0.0",
) -> str:
    """Calculate next release version of the MongoDB Kubernetes Operator.

    Parameters:
        repository_path: Path to the Git repository. Default is the current directory '.'.
        changelog_sub_path: Path to the changelog directory relative to the repository root. Default is 'changelog/'.
        initial_commit_sha: SHA of the initial commit to start from if no previous version tag is found.
        initial_version: Version to use if no previous version tag is found. Default is "1.0.0".

    Returns:
        Formatted release notes as a string.
    """
    repo = Repo(repository_path)

    version, _ = calculate_next_version_with_changelog(repo, changelog_sub_path, initial_commit_sha, initial_version)

    return version


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--path",
        action="store",
        default=".",
        type=pathlib.Path,
        help="Path to the Git repository. Default is the current directory '.'",
    )
    parser.add_argument(
        "--changelog_path",
        default="changelog/",
        action="store",
        type=str,
        help="Path to the changelog directory relative to the repository root. Default is 'changelog/'",
    )
    parser.add_argument(
        "--initial_commit_sha",
        action="store",
        type=str,
        help="SHA of the initial commit to start from if no previous version tag is found.",
    )
    parser.add_argument(
        "--initial_version",
        default="1.0.0",
        action="store",
        type=str,
        help="Version to use if no previous version tag is found. Default is '1.0.0'",
    )
    parser.add_argument("--output", "-o", type=pathlib.Path)
    args = parser.parse_args()

    version, _ = next_release_version(args.path, args.changelog_path, args.initial_commit_sha, args.initial_version)

    print(version)
