import semver
from git import Commit, Repo

from scripts.release.changelog import ChangeType

def find_previous_version(initial_version: str, initial_commit_sha: str, repository_path: str = '.', ) -> tuple[str, Commit]:
    repo = Repo(repository_path)
    head_commit = repo.head.commit

    # Filter tags that are ancestors of the current HEAD commit
    ancestor_tags = filter(lambda t: repo.is_ancestor(t.commit, head_commit) and t.commit != head_commit, repo.tags)

    # Filter valid SemVer tags and sort them
    valid_tags = filter(lambda t: semver.VersionInfo.is_valid(t.name), ancestor_tags)
    sorted_tags: list = sorted(valid_tags, key=lambda t: semver.VersionInfo.parse(t.name), reverse=True)

    if not sorted_tags:
        # Find the initial commit by traversing to the earliest commit reachable from HEAD
        return initial_version, repo.git.rev_parse(initial_commit_sha)

    return sorted_tags[0].name, sorted_tags[0].commit

def calculate_next_release_version(previous_version_str: str, changelog: list[ChangeType]) -> str:
    previous_version = semver.VersionInfo.parse(previous_version_str)

    if ChangeType.BREAKING in changelog:
        return str(previous_version.bump_major())

    if ChangeType.FEATURE in changelog:
        return str(previous_version.bump_minor())

    return str(previous_version.bump_patch())
