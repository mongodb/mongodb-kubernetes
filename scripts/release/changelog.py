import os
from enum import StrEnum

import git
from git import Repo

CHANGELOG_PATH = "changelog/"

BREAKING_CHANGE_ENTRIES = ["breaking_change", "major"]
FEATURE_ENTRIES = ["feat", "feature"]
BUGFIX_ENTRIES = ["fix", "bugfix", "hotfix", "patch"]


class ChangeType(StrEnum):
    FEATURE = 'feature'
    BREAKING = 'breaking'
    FIX = 'fix'
    OTHER = 'other'

def generate_changelog(previous_version: str, repository_path: str = '.') -> str:
    """Generate a changelog based on the changes since the previous version tag."""
    changelog_entries = get_changelog_entries(previous_version, repository_path)

    if not changelog_entries:
        return "No changes since the previous version."

    changelog = []
    for change_type, content in changelog_entries:
        changelog.append(f"## {change_type.capitalize()}\n\n{content.strip()}\n")

    return "\n".join(changelog)

def get_changelog_entries(previous_version: str, repository_path: str = '.') -> list[tuple[ChangeType, str]]:
    changelog = []

    repo = Repo(repository_path)

    # Find the commit object for the previous version tag
    try:
        tag_ref = repo.tags[previous_version]
    except IndexError:
        raise ValueError(f"Tag '{previous_version}' not found")

    # Compare previous version commit with current working tree
    # TODO: or compare with head commit?
    diff_index = tag_ref.commit.diff(git.INDEX, paths=CHANGELOG_PATH)

    # No changes since the previous version
    if not diff_index:
        return changelog

    # Traverse added Diff objects only (change type 'A' for added files)
    for diff_item in diff_index.iter_change_type("A"):
        file_path = diff_item.b_path
        file_name = os.path.basename(file_path)
        change_type = get_change_type(file_name)

        with open(file_path, 'r') as file:
            file_content = file.read()

        changelog.append((change_type, file_content))

    return changelog


def get_change_type(file_name: str) -> ChangeType:
    """Extract the change type from the file name."""

    if any(entry in file_name.lower() for entry in BREAKING_CHANGE_ENTRIES):
        return ChangeType.BREAKING
    elif any(entry in file_name.lower() for entry in FEATURE_ENTRIES):
        return ChangeType.FEATURE
    elif any(entry in file_name.lower() for entry in BUGFIX_ENTRIES):
        return ChangeType.FIX
    else:
        return ChangeType.OTHER
