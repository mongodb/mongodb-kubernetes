import os
from dataclasses import dataclass
from typing import Optional

from scripts.release.build.build_scenario import BuildScenario


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
