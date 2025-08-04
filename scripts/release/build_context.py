import os
from dataclasses import dataclass
from enum import Enum
from typing import Optional

from lib.base_logger import logger


class BuildScenario(str, Enum):
    """Represents the context in which the build is running."""

    RELEASE = "release"  # Official release triggered by a git tag
    PATCH = "patch"  # CI build for a patch/pull request
    STAGING = "staging"  # CI build from a merge to the master
    DEVELOPMENT = "development"  # Local build on a developer machine

    @classmethod
    def infer_scenario_from_environment(cls) -> "BuildScenario":
        """Infer the build scenario from environment variables."""
        git_tag = os.getenv("triggered_by_git_tag")
        is_patch = os.getenv("is_patch", "false").lower() == "true"
        is_evg = os.getenv("RUNNING_IN_EVG", "false").lower() == "true"
        patch_id = os.getenv("version_id")

        if git_tag:
            # Release scenario and the git tag will be used for promotion process only
            scenario = BuildScenario.RELEASE
            logger.info(f"Build scenario: {scenario} (git_tag: {git_tag})")
        elif is_patch:
            scenario = BuildScenario.PATCH
            logger.info(f"Build scenario: {scenario} (patch_id: {patch_id})")
        elif is_evg:
            scenario = BuildScenario.STAGING
            logger.info(f"Build scenario: {scenario} (patch_id: {patch_id})")
        else:
            scenario = BuildScenario.DEVELOPMENT
            logger.info(f"Build scenario: {scenario}")

        return scenario


@dataclass
class BuildContext:
    """Define build parameters based on the build scenario."""

    scenario: BuildScenario
    git_tag: Optional[str] = None
    patch_id: Optional[str] = None
    signing_enabled: bool = False
    multi_arch: bool = True
    version: Optional[str] = None

    @classmethod
    def from_scenario(cls, scenario: BuildScenario) -> "BuildContext":
        """Create build context from a given scenario."""
        git_tag = os.getenv("triggered_by_git_tag")
        patch_id = os.getenv("version_id")
        signing_enabled = scenario == BuildScenario.RELEASE

        return cls(
            scenario=scenario,
            git_tag=git_tag,
            patch_id=patch_id,
            signing_enabled=signing_enabled,
            version=git_tag or patch_id,
        )

    def get_version(self) -> str:
        """Gets the version that will be used to tag the images."""
        if self.scenario == BuildScenario.RELEASE:
            return self.git_tag
        if self.patch_id:
            return self.patch_id
        return "latest"

    def get_base_registry(self) -> str:
        """Get the base registry URL for the current scenario."""
        # TODO CLOUDP-335471: when working on the promotion process, use the prod registry variable in RELEASE scenario
        if self.scenario == BuildScenario.STAGING:
            return os.environ.get("STAGING_REPO_URL")
        else:
            return os.environ.get("BASE_REPO_URL")
