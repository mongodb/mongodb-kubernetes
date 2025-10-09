from enum import StrEnum


class BuildScenario(StrEnum):
    RELEASE = "release"  # Official release triggered by a git tag
    MANUAL_RELEASE = "manual_release"  # Manual release, not part of operator release cycle
    PATCH = "patch"  # CI build for a patch/pull request
    STAGING = "staging"  # CI build from a merge to the master
    DEVELOPMENT = "development"  # Local build on a developer machine
