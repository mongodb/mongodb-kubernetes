import semver
from git import Repo, TagReference

from scripts.release.changelog import ChangeType


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
