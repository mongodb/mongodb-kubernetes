import os
from dataclasses import dataclass
from enum import Enum
from typing import Optional

from lib.base_logger import logger


class BuildScenario(str, Enum):
    """Represents the context in which the build is running."""

    RELEASE = "release"  # Official release build from a git tag
    PATCH = "patch"  # CI build for a patch/pull request
    MASTER = "master"  # CI build from a merge to the master
    DEVELOPMENT = "development"  # Local build on a developer machine


@dataclass
class BuildContext:
    """Holds the context of the current build, detected from the environment."""

    scenario: BuildScenario
    git_tag: Optional[str] = None
    patch_id: Optional[str] = None
    signing_enabled: bool = False
    multi_arch: bool = True

    @classmethod
    def from_environment(cls) -> "BuildContext":
        """Auto-detect build context from environment variables."""
        scenario = BuildScenario.DEVELOPMENT
        signing = False

        git_tag = os.getenv("triggered_by_git_tag")
        is_patch = os.getenv("is_patch", "false").lower() == "true"
        is_evg = os.getenv("RUNNING_IN_EVG", "false").lower() == "true"
        patch_id = os.getenv("version_id")

        if git_tag:
            scenario = BuildScenario.RELEASE
            signing = True
            logger.info(f"Build scenario: {scenario} (git_tag: {git_tag})")
        elif is_patch:
            scenario = BuildScenario.PATCH
            logger.info(f"Build scenario: {scenario} (patch_id: {patch_id})")
        elif is_evg:
            scenario = BuildScenario.MASTER
            logger.info(f"Build scenario: {scenario} (patch_id: {patch_id})")
        else:
            scenario = BuildScenario.DEVELOPMENT
            logger.info(f"Build scenario: {scenario}")

        return cls(
            scenario=scenario,
            git_tag=git_tag,
            patch_id=patch_id,
            signing_enabled=signing,
        )


class VersionResolver:
    """Determines the correct version/tag for an image based on the build context."""

    def __init__(self, build_context: BuildContext):
        self.context = build_context

    def get_version(self) -> str:
        """Gets the primary version string for the current build."""
        if self.context.scenario == BuildScenario.RELEASE:
            return self.context.git_tag
        if self.context.patch_id:
            return self.context.patch_id
        return "latest"


class RegistryResolver:
    """Determines the correct container registry based on the build context."""

    ECR_BASE_URL = "268558157000.dkr.ecr.us-east-1.amazonaws.com"

    REGISTRY_MAP = {
        BuildScenario.RELEASE: f"{ECR_BASE_URL}/julienben/staging-temp",  # TODO: replace with real staging repo
        BuildScenario.MASTER: f"{ECR_BASE_URL}/dev",
        BuildScenario.PATCH: f"{ECR_BASE_URL}/dev",
        BuildScenario.DEVELOPMENT: os.environ.get("BASE_REPO_URL"),
    }

    def __init__(self, build_context: BuildContext):
        self.context = build_context

    def get_base_registry(self) -> str:
        """Get the base registry URL for the current build scenario."""
        ## Allow overriding registry for development and testing
        # override_registry = os.environ.get("QUAY_REGISTRY")
        # if self.context.scenario == BuildScenario.DEVELOPMENT and override_registry:
        #    logger.info(f"Using override registry: {override_registry}")
        #    return override_registry

        registry = self.REGISTRY_MAP.get(self.context.scenario)
        if not registry:
            raise ValueError(f"No registry defined for scenario {self.context.scenario}")
        logger.info(f"Using registry: {registry}")
        return registry
