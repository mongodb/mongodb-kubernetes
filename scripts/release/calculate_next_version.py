import argparse
import pathlib

from git import Repo

from scripts.release.constants import (
    DEFAULT_CHANGELOG_PATH,
    DEFAULT_RELEASE_INITIAL_VERSION,
    DEFAULT_REPOSITORY_PATH,
)
from scripts.release.version import calculate_next_version

if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Calculate the next version based on the changes since the previous version tag.",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "-p",
        "--path",
        default=DEFAULT_REPOSITORY_PATH,
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
        default=DEFAULT_RELEASE_INITIAL_VERSION,
        metavar="",
        action="store",
        type=str,
        help=f"Version to use if no previous version tag is found. Default is '{DEFAULT_RELEASE_INITIAL_VERSION}'",
    )
    args = parser.parse_args()

    repo = Repo(args.path)

    version = calculate_next_version(repo, args.changelog_path, args.initial_commit_sha, args.initial_version)

    print(version)
