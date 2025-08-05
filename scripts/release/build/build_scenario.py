from enum import StrEnum


class BuildScenario(StrEnum):
    RELEASE = "release"  # Official release triggered by a git tag
    PATCH = "patch"  # CI build for a patch/pull request
    STAGING = "staging"  # CI build from a merge to the master
