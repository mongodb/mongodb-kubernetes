import os
from enum import StrEnum


class BuildScenario(StrEnum):
    RELEASE = "release"  # Official release triggered by a git tag or OM version bump
    PATCH = "patch"  # CI build for a patch/pull request
    STAGING = "staging"  # CI build from a merge to the master
    DEVELOPMENT = "development"  # Local build on a developer machine
    DRYRUN_RELEASE = (
        "dryrun-release"  # Release pipeline dry-run: release behavior, staging destination, no prod side effects
    )


def is_dryrun() -> bool:
    """True when the release pipeline runs as a dry-run (BUILD_SCENARIO=dryrun-release patch param)."""
    return os.environ.get("BUILD_SCENARIO") == BuildScenario.DRYRUN_RELEASE


SUPPORTED_SCENARIOS = supported_scenarios = list(BuildScenario)
