import argparse
import pathlib
import sys

from git import Repo

from scripts.release.release_notes import calculate_next_version_with_changelog

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--path", action="store", default=".", type=pathlib.Path,
                        help="Path to the Git repository. Default is the current directory '.'")
    parser.add_argument("--changelog_path", default="changelog/", action="store", type=str,
                        help="Path to the changelog directory relative to the repository root. Default is 'changelog/'")
    parser.add_argument("--initial_commit_sha", action="store", type=str,
                        help="SHA of the initial commit to start from if no previous version tag is found.")
    parser.add_argument("--initial_version", default="1.0.0", action="store", type=str,
                        help="Version to use if no previous version tag is found. Default is '1.0.0'")
    parser.add_argument("--output", "-o", type=pathlib.Path)
    args = parser.parse_args()

    repo = Repo(args.path)

    version, _ = calculate_next_version_with_changelog(repo, args.changelog_path, args.initial_commit_sha,
                                                       args.initial_version)

    print(version)
