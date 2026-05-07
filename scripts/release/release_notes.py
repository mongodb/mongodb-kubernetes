import argparse
import pathlib

from git import Repo
from jinja2 import Template

from scripts.release.changelog import ChangeKind
from scripts.release.constants import DEFAULT_CHANGELOG_PATH, DEFAULT_RELEASE_INITIAL_VERSION, DEFAULT_REPOSITORY_PATH
from scripts.release.version import calculate_next_version_with_changelog


def generate_release_notes(
    repository_path: str,
    changelog_sub_path: str,
    initial_commit_sha: str = None,
    initial_version: str = None,
) -> str:
    f"""Generate a release notes based on the changes since the previous version tag.

    Parameters:
        repository_path: Path to the Git repository.
        changelog_sub_path: Path to the changelog directory relative to the repository root.
        initial_commit_sha: SHA of the initial commit to start from if no previous version tag is found.
        initial_version: Version to use if no previous version tag is found.

    Returns:
        Formatted release notes as a string.
    """
    repo = Repo(repository_path)

    version, changelog = calculate_next_version_with_changelog(
        repo, changelog_sub_path, initial_commit_sha, initial_version
    )

    with open("scripts/release/release_notes_tpl.md", "r") as f:
        template = Template(f.read())

    parameters = {
        "version": version,
        "preludes": [c.contents for c in changelog if c.kind == ChangeKind.PRELUDE],
        "breaking_changes": [c.contents for c in changelog if c.kind == ChangeKind.BREAKING],
        "features": [c.contents for c in changelog if c.kind == ChangeKind.FEATURE],
        "fixes": [c.contents for c in changelog if c.kind == ChangeKind.FIX],
        "others": [c.contents for c in changelog if c.kind == ChangeKind.OTHER],
    }

    return template.render(parameters)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Generate release notes based on the changes since the previous version tag.",
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
        help=f"Path to the changelog directory relative to a current working directory. Default is '{DEFAULT_CHANGELOG_PATH}'",
    )
    parser.add_argument(
        "-s",
        "--initial-commit-sha",
        metavar="",
        action="store",
        type=str,
        help="SHA of the initial commit to start from if no previous version tag is found.",
    )
    parser.add_argument(
        "-v",
        "--initial-version",
        default=DEFAULT_RELEASE_INITIAL_VERSION,
        metavar="",
        action="store",
        type=str,
        help=f"Version to use if no previous version tag is found. Default is '{DEFAULT_RELEASE_INITIAL_VERSION}'",
    )
    parser.add_argument(
        "--output",
        "-o",
        metavar="",
        type=pathlib.Path,
        help="Path to save the release notes. If not provided, prints to stdout.",
    )
    args = parser.parse_args()

    release_notes = generate_release_notes(
        args.path, args.changelog_path, args.initial_commit_sha, args.initial_version
    )

    if args.output is not None:
        with open(args.output, "w") as file:
            file.write(release_notes)
    else:
        print(release_notes)
