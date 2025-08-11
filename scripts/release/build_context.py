import os
from dataclasses import dataclass
from enum import Enum
from typing import Optional

from lib.base_logger import logger


class BuildScenario(str, Enum):
    """Represents the context in which the build is running."""

    RELEASE = "release"  # Official release triggered by a git tag
    PATCH = "patch"  # CI build for a patch/pull request
    STAGING = "staging"  # CI build from a merge to the master branch
    DEVELOPMENT = "development"  # Local build on a developer machine

    @classmethod
    def infer_scenario_from_environment(cls) -> "BuildScenario":
        """Infer the build scenario from environment variables."""
        git_tag = os.getenv("triggered_by_git_tag")
        # is_patch is passed automatically by Evergreen.
        # It is "true" if the running task is in a patch build and undefined if it is not.
        # A patch build is a version not triggered by a commit to a repository.
        # It either runs tasks on a base commit plus some diff if submitted by the CLI or on a git branch if created by
        # a GitHub pull request.
        is_patch = os.getenv("is_patch", "false").lower() == "true"
        # RUNNING_IN_EVG is set by us in evg-private-context
        is_evg = os.getenv("RUNNING_IN_EVG", "false").lower() == "true"
        # version_id is the id of the task's version. It is generated automatically for each task run.
        # For example: `6899b7e35bfaee00077db986` for a manual/PR patch,
        # or `mongodb_kubernetes_5c5a3accb47bb411682b8c67f225b61f7ad5a619` for a master merge
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
        if self.scenario == BuildScenario.STAGING:
            # On master merges, always use "latest" (preserving legacy behavior)
            return self.patch_id
        if self.patch_id:
            return self.patch_id
        # Alternatively, we can fail here if no ID is explicitly defined
        # When working locally, "version_id" env variable is defined in the generated context file. It is "latest" by
        # default, and can be overridden with OVERRIDE_VERSION_ID
        return "latest"

    def get_base_registry(self) -> str:
        """Get the base registry URL for the current scenario."""
        # TODO CLOUDP-335471: when working on the promotion process, use the prod registry variable in RELEASE scenario
        # TODO CLOUDP-335471: STAGING scenario should also push to STAGING_REPO_URL with version_id tag,
        #                     in addition to the current ECR dev latest push (for backward compatibility)
        #                     This will enable proper staging environment testing before production releases

        # For now, always use BASE_REPO_URL to preserve legacy behavior
        # (STAGING pushes to ECR dev with "latest" tag)
        return os.environ.get("BASE_REPO_URL")
