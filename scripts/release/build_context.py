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


def infer_scenario_from_environment() -> BuildScenario:
    """Infer the build scenario from environment variables."""
    git_tag = os.getenv("triggered_by_git_tag")
    is_patch = os.getenv("is_patch", "false").lower() == "true"
    is_evg = os.getenv("RUNNING_IN_EVG", "false").lower() == "true"
    patch_id = os.getenv("version_id")

    if git_tag:
        scenario = BuildScenario.RELEASE
        logger.info(f"Build scenario: {scenario} (git_tag: {git_tag})")
    elif is_patch:
        scenario = BuildScenario.PATCH
        logger.info(f"Build scenario: {scenario} (patch_id: {patch_id})")
    elif is_evg:
        scenario = BuildScenario.MASTER  # TODO: ultimately we won't have RELEASE variant and master will push to staging
        logger.info(f"Build scenario: {scenario} (patch_id: {patch_id})")
    else:
        scenario = BuildScenario.DEVELOPMENT
        logger.info(f"Build scenario: {scenario}")

    return scenario


@dataclass
class BuildContext:
    """Holds the context of the current build, detected from the environment."""

    ECR_BASE_URL = "268558157000.dkr.ecr.us-east-1.amazonaws.com"
    
    REGISTRY_MAP = {
        BuildScenario.RELEASE: f"{ECR_BASE_URL}/julienben/staging-temp",  # TODO: replace with real staging repo
        BuildScenario.MASTER: f"{ECR_BASE_URL}/dev",
        BuildScenario.PATCH: f"{ECR_BASE_URL}/dev",
        BuildScenario.DEVELOPMENT: os.environ.get("BASE_REPO_URL"),
    }

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

    @classmethod
    def from_environment(cls) -> "BuildContext":
        """Auto-detect build context from environment variables."""
        scenario = infer_scenario_from_environment()
        return cls.from_scenario(scenario)
    
    def get_version(self) -> str:
        """Gets the primary version string for the current build."""
        if self.scenario == BuildScenario.RELEASE:
            return self.git_tag
        if self.patch_id:
            return self.patch_id
        return "latest"
    
    def get_base_registry(self) -> str:
        """Get the base registry URL for the current build scenario."""
        registry = self.REGISTRY_MAP.get(self.scenario)
        if not registry:
            raise ValueError(f"No registry defined for scenario {self.scenario}")
        logger.info(f"Using registry: {registry}")
        return registry

