import semver

from scripts.release.changelog import ChangeType


def calculate_next_release_version(previous_version_str: str, changelog: list[ChangeType]) -> str:
    previous_version = semver.VersionInfo.parse(previous_version_str)

    if ChangeType.BREAKING in changelog:
        return str(previous_version.bump_major())

    if ChangeType.FEATURE in changelog:
        return str(previous_version.bump_minor())

    return str(previous_version.bump_patch())
