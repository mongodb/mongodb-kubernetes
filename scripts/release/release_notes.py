from git import Repo
from jinja2 import Template

from scripts.release.changelog import CHANGELOG_PATH, get_changelog_entries, ChangeType
from scripts.release.versioning import calculate_next_release_version, find_previous_version


def generate_release_notes(
    repository_path: str = '.',
    changelog_sub_path: str = CHANGELOG_PATH,
) -> str:
    """Generate a release notes based on the changes since the previous version tag."""
    repo = Repo(repository_path)
    initial_commit = list(repo.iter_commits(reverse=True))[0].hexsha

    previous_version, previous_commit = find_previous_version("0.0.0", initial_commit, repository_path)

    changelog: list = get_changelog_entries(previous_commit, repository_path, changelog_sub_path)

    changelog_entries = list[ChangeType](map(lambda x: x[0], changelog))
    version = calculate_next_release_version(previous_version, changelog_entries)

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
