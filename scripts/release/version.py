import semver
from git import Commit, Repo, TagReference

from scripts.release.changelog import ChangeType

DEFAULT_SUPPORTED_OPERATOR_VERSIONS = 3

def supported_operator_versions(repo: Repo, current_version: str, supported_versions: int = DEFAULT_SUPPORTED_OPERATOR_VERSIONS) -> list[str]:
    sorted_tags = find_previous_version_tags_ordered(repo)
    operator_versions = list(get_release()["supportedImages"]["mongodb-kubernetes"]["versions"])
    operator_versions.sort(key=lambda s: list(map(int, s.split("."))))

    if len(operator_versions) <= supported_versions:
        return operator_versions

    return operator_versions[-supported_versions:]


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
    sorted_tags = find_previous_version_tags_ordered(repo)

    if not sorted_tags:
        return None

    return sorted_tags[0]


def find_previous_version_tags_ordered(repo: Repo) -> list[TagReference] | None:
    """Find the recent version tags that are ancestors of the current HEAD commit, ordered by SemVer."""

    head_commit = repo.head.commit

    # Filter tags that are ancestors of the current HEAD commit
    ancestor_tags = filter(lambda t: repo.is_ancestor(t.commit, head_commit) and t.commit != head_commit, repo.tags)

    # Filter valid SemVer tags and sort them
    valid_tags = filter(lambda t: semver.VersionInfo.is_valid(t.name), ancestor_tags)
    sorted_tags: list[TagReference] = sorted(valid_tags, key=lambda t: semver.VersionInfo.parse(t.name), reverse=True)

    return sorted_tags


def calculate_next_release_version(previous_version_str: str, changelog: list[ChangeType]) -> str:
    previous_version = semver.VersionInfo.parse(previous_version_str)

    if ChangeType.BREAKING in changelog:
        return str(previous_version.bump_major())

    if ChangeType.FEATURE in changelog:
        return str(previous_version.bump_minor())

    return str(previous_version.bump_patch())
