import os
from enum import StrEnum

import semver
from git import Commit, Repo, TagReference

from scripts.release.changelog import (
    DEFAULT_CHANGELOG_PATH,
    ChangeEntry,
    ChangeKind,
    get_changelog_entries,
)


class Environment(StrEnum):
    DEV = "dev"
    STAGING = "staging"
    PROD = "prod"


def get_version_for_environment(env: Environment, initial_commit_sha: str | None, initial_version: str,
                                repository_path: str = ".",
                                changelog_sub_path: str = DEFAULT_CHANGELOG_PATH) -> str:
    repo = Repo(repository_path)

    match env:
        case Environment.DEV:
            build_id = os.environ["BUILD_ID"]
            if not build_id:
                raise ValueError(f"BUILD_ID environment variable is not set for {env} environment")
            return build_id
        case Environment.STAGING:
            return repo.head.object.hexsha
        case Environment.PROD:
            return calculate_next_version(repo, changelog_sub_path, initial_commit_sha, initial_version)

    raise ValueError(f"Unknown environment: {env}")


def calculate_next_version(repo: Repo, changelog_sub_path: str, initial_commit_sha: str | None,
                           initial_version: str) -> str:
    return calculate_next_version_with_changelog(repo, changelog_sub_path, initial_commit_sha, initial_version)[0]


def calculate_next_version_with_changelog(
    repo: Repo, changelog_sub_path: str, initial_commit_sha: str | None, initial_version: str
) -> (str, list[ChangeEntry]):
    previous_version_tag, previous_version_commit = find_previous_version(repo, initial_commit_sha)

    changelog: list[ChangeEntry] = get_changelog_entries(previous_version_commit, repo, changelog_sub_path)
    changelog_kinds = list(set(entry.kind for entry in changelog))

    # If there is no previous version tag, we start with the initial version tag
    if not previous_version_tag:
        version = initial_version
    else:
        version = increment_previous_version(previous_version_tag.name, changelog_kinds)

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


def increment_previous_version(previous_version_str: str, changelog: list[ChangeKind]) -> str:
    previous_version = semver.VersionInfo.parse(previous_version_str)

    if ChangeKind.BREAKING in changelog:
        return str(previous_version.bump_major())

    if ChangeKind.FEATURE in changelog:
        return str(previous_version.bump_minor())

    return str(previous_version.bump_patch())
