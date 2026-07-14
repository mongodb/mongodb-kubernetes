from dataclasses import dataclass

import semver
from git import BadName, Commit, Repo

from scripts.release.changelog import ChangeEntry, ChangeKind, get_changelog_entries


@dataclass
class VersionTag:
    name: str
    commit: Commit
    is_initial: bool = False


def calculate_next_version(
    repo: Repo, changelog_sub_path: str, initial_commit_sha: str | None, initial_version: str | None
) -> str:
    return calculate_next_version_with_changelog(repo, changelog_sub_path, initial_commit_sha, initial_version)[0]


def calculate_next_version_with_changelog(
    repo: Repo, changelog_sub_path: str, initial_commit_sha: str | None, initial_version: str | None
) -> tuple[str, list[ChangeEntry]]:
    previous_version_tag = find_previous_version(
        repo=repo,
        initial_commit_sha=initial_commit_sha,
        initial_version=initial_version,
    )

    changelog: list[ChangeEntry] = get_changelog_entries(previous_version_tag.commit, repo, changelog_sub_path)
    changelog_kinds = list(set(entry.kind for entry in changelog))

    # If we start with the initial version tag, do not increment it
    if previous_version_tag.is_initial:
        version = previous_version_tag.name
    else:
        version = increment_previous_version(previous_version_tag.name, changelog_kinds)

    return version, changelog


def find_previous_version(repo: Repo, initial_commit_sha: str = None, initial_version: str = None) -> VersionTag:
    """Find the most recent version that is an ancestor of the current HEAD commit."""

    previous_version_tag = find_previous_version_tag(repo)
    if previous_version_tag:
        return previous_version_tag

    # If there is no previous version tag, we start with the initial commit
    # If no initial commit SHA provided, use the first commit in the repository
    if not initial_commit_sha:
        initial_commit_sha = list(repo.iter_commits(reverse=True))[0].hexsha

    if not initial_version:
        raise ValueError("No previous version tag found and no initial version provided.")

    return VersionTag(initial_version, repo.commit(initial_commit_sha), True)


def find_previous_version_tag(repo: Repo) -> VersionTag | None:
    """Find the most recent version tag on remote origin that is an ancestor of the current HEAD commit."""

    head_commit = repo.head.commit

    # Filter tags that are ancestors of the current HEAD commit
    ancestor_tags = filter(
        lambda t: repo.is_ancestor(t.commit, head_commit) and t.commit != head_commit, get_remote_tags(repo)
    )

    # Filter valid SemVer tags and sort them
    valid_tags = filter(lambda t: semver.VersionInfo.is_valid(t.name), ancestor_tags)
    sorted_tags = sorted(valid_tags, key=lambda t: semver.VersionInfo.parse(t.name), reverse=True)

    if not sorted_tags:
        return None

    return sorted_tags[0]


def get_remote_tags(repo: Repo) -> list[VersionTag]:
    """Returns VersionTags from remote origin without modifying local state.

    Annotated tags are dereferenced to their target commit SHA via the ^{} ls-remote lines.
    Tags whose commits are not present locally are skipped.
    """

    if not any(r.name == "origin" for r in repo.remotes):
        return []

    ls_output = repo.git.ls_remote("--tags", "origin")
    if not ls_output:
        return []

    dereferenced: dict[str, str] = {}
    direct: dict[str, str] = {}

    for line in ls_output.splitlines():
        sha, ref = line.split("\t")
        if ref.endswith("^{}"):
            name = ref.removeprefix("refs/tags/").removesuffix("^{}")
            dereferenced[name] = sha
        elif ref.startswith("refs/tags/"):
            name = ref.removeprefix("refs/tags/")
            direct[name] = sha

    # Annotated tags appear twice: tag-object SHA and dereferenced (^{}) commit SHA.
    # Use the dereferenced SHA when available; otherwise direct (lightweight tag).
    resolved = {name: dereferenced.get(name, sha) for name, sha in direct.items()}

    tags = []
    for name, sha in resolved.items():
        try:
            tags.append(VersionTag(name=name, commit=repo.commit(sha)))
        except BadName:
            pass
    return tags


def increment_previous_version(previous_version_str: str, changelog: list[ChangeKind]) -> str:
    previous_version = semver.VersionInfo.parse(previous_version_str)

    if ChangeKind.BREAKING in changelog:
        return str(previous_version.bump_major())

    if ChangeKind.FEATURE in changelog:
        return str(previous_version.bump_minor())

    return str(previous_version.bump_patch())
