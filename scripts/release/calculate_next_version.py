import argparse
import pathlib

from git import Repo

from scripts.release.changelog import (
    DEFAULT_CHANGELOG_PATH,
    DEFAULT_INITIAL_GIT_TAG_VERSION,
)
from scripts.release.release_notes import calculate_next_version_with_changelog

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "-p",
        "--path",
        default=".",
        metavar="",
        action="store",
        type=pathlib.Path,
        help="Path to the Git repository. Default is the current directory '.'",
    )
    parser.add_argument(
        "-c",
        "--changelog-path",
        default=DEFAULT_CHANGELOG_PATH,
        metavar="",
        action="store",
        type=str,
        help=f"Path to the changelog directory relative to the repository root. Default is '{DEFAULT_CHANGELOG_PATH}'",
    )
    parser.add_argument(
        "-s",
        "--initial-commit-sha",
        metavar="",
        action="store",
        type=str,
        help="SHA of the initial commit to start from if no previous version tag is found.",
    )
    parser.add_argument(
        "-v",
        "--initial-version",
        default=DEFAULT_INITIAL_GIT_TAG_VERSION,
        metavar="",
        action="store",
        type=str,
        help=f"Version to use if no previous version tag is found. Default is '{DEFAULT_INITIAL_GIT_TAG_VERSION}'",
    )
    parser.add_argument("--output", "-o", type=pathlib.Path)
    args = parser.parse_args()

    repo = Repo(args.path)

    version, _ = calculate_next_version_with_changelog(
        repo, args.changelog_path, args.initial_commit_sha, args.initial_version
    )

    print(version)
