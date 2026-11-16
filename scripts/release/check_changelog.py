import argparse
import pathlib

from git import Repo

from scripts.release.argparse_utils import str2bool
from scripts.release.changelog import get_changelog_entries
from scripts.release.constants import DEFAULT_CHANGELOG_PATH, DEFAULT_REPOSITORY_PATH

if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Check if there are changelog entries",
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
        "-b",
        "--base-sha",
        metavar="",
        action="store",
        required=True,
        type=str,
        help="Base commit SHA to compare against. This should be the SHA of the base branch the Pull Request is targeting.",
    )
    parser.add_argument(
        "-f",
        "--fail-if-no-changes",
        default=True,
        metavar="",
        action="store",
        type=str2bool,
        nargs="?",
        help="Fail if no changelog entries are found. Default is True.",
    )
    args = parser.parse_args()

    repo = Repo(args.path)
    base_commit = repo.commit(args.base_sha)

    try:
        changelog = get_changelog_entries(base_commit, repo, args.changelog_path)
    except Exception as e:
        print(f"Error retrieving changelog entries. Possible validation issues: {e}")
        exit(1)

    if not changelog:
        print("No changelog entries found.")
        if args.fail_if_no_changes:
            print("Exiting with error due to no changelog entries found.")
            exit(1)
    else:
        print("Changelog entries found and validated")
