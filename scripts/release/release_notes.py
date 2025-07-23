import argparse
import pathlib
import sys

from git import Repo
from jinja2 import Template

from scripts.release.changelog import CHANGELOG_PATH, get_changelog_entries, ChangeType
from scripts.release.version import calculate_next_release_version, find_previous_version


def generate_release_notes(
    repository_path: str = '.',
    changelog_sub_path: str = CHANGELOG_PATH,
    initial_commit_sha: str = None,
    initial_version: str = "1.0.0",
) -> str:
    """Generate a release notes based on the changes since the previous version tag.

    Parameters:
        repository_path: Path to the Git repository. Default is the current directory '.'.
        changelog_sub_path: Path to the changelog directory relative to the repository root. Default is 'changelog/'.
        initial_commit_sha: SHA of the initial commit to start from if no previous version tag is found.
        initial_version: Version to use if no previous version tag is found. Default is "1.0.0".

    Returns:
        Formatted release notes as a string.
    """
    repo = Repo(repository_path)

    version, changelog = calculate_next_version_with_changelog(repo, changelog_sub_path, initial_commit_sha,
                                                               initial_version)

    with open('scripts/release/release_notes_tpl.md', "r") as f:
        template = Template(f.read())

    parameters = {
        'version': version,
        'preludes': [c[1] for c in changelog if c[0] == ChangeType.PRELUDE],
        'breaking_changes': [c[1] for c in changelog if c[0] == ChangeType.BREAKING],
        'features': [c[1] for c in changelog if c[0] == ChangeType.FEATURE],
        'fixes': [c[1] for c in changelog if c[0] == ChangeType.FIX],
        'others': [c[1] for c in changelog if c[0] == ChangeType.OTHER],
    }

    return template.render(parameters)


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

    release_notes = generate_release_notes(args.path, args.changelog_path, args.initial_commit_sha,
                                           args.initial_version)

    if args.output is not None:
        with open(args.output, "w") as file:
            file.write(release_notes)
    else:
        print(release_notes)
