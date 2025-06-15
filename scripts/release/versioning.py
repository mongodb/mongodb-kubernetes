import argparse
import pathlib
import sys

import semver
from git import Repo, TagReference, Commit

from scripts.release.changelog import ChangeType, get_changelog_entries


def calculate_next_version_with_changelog(
        repo: Repo,
        changelog_sub_path: str,
        initial_commit_sha: str | None,
        initial_version: str) -> (str, list[tuple[ChangeType, str]]):
    previous_version_tag, previous_version_commit = find_previous_version(repo, initial_commit_sha)

    changelog: list[tuple[ChangeType, str]] = get_changelog_entries(previous_version_commit, repo, changelog_sub_path)
    changelog_types = list[ChangeType](map(lambda x: x[0], changelog))

    # If there is no previous version tag, we start with the initial version tag
    if not previous_version_tag:
        version = initial_version
    else:
        version = calculate_next_release_version(previous_version_tag.name, changelog_types)

    return version, changelog


def find_previous_version(repo: Repo, initial_commit_sha: str = None) -> (TagReference | None, Commit):
    """Find the most recent version that is an ancestor of the current HEAD commit."""

    previous_version_tag = find_previous_version_tag(repo)

    # If there is no previous version tag, we start with the initial commit
    if not previous_version_tag:
        # If no initial commit SHA provided, use the first commit in the repository
        if not initial_commit_sha:
            initial_commit_sha = list(repo.iter_commits(reverse=True))[0].hexsha

        return None, repo.commit(initial_commit_sha)

    return previous_version_tag, previous_version_tag.commit


def find_previous_version_tag(repo: Repo) -> TagReference | None:
    """Find the most recent version tag that is an ancestor of the current HEAD commit."""

    head_commit = repo.head.commit

    # Filter tags that are ancestors of the current HEAD commit
    ancestor_tags = filter(lambda t: repo.is_ancestor(t.commit, head_commit) and t.commit != head_commit, repo.tags)

    # Filter valid SemVer tags and sort them
    valid_tags = filter(lambda t: semver.VersionInfo.is_valid(t.name), ancestor_tags)
    sorted_tags = sorted(valid_tags, key=lambda t: semver.VersionInfo.parse(t.name), reverse=True)

    if not sorted_tags:
        return None

    return sorted_tags[0]


def calculate_next_release_version(previous_version_str: str, changelog: list[ChangeType]) -> str:
    previous_version = semver.VersionInfo.parse(previous_version_str)

    if ChangeType.BREAKING in changelog:
        return str(previous_version.bump_major())

    if ChangeType.FEATURE in changelog:
        return str(previous_version.bump_minor())

    return str(previous_version.bump_patch())


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

    _, version = calculate_next_version_with_changelog(args.path, args.changelog_path, args.initial_commit_sha,
                                                       args.initial_version)

    sys.stdout.write(version)
