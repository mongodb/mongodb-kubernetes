from dataclasses import dataclass
from typing import List, Optional

from scripts.release.build.build_scenario import BuildScenario

SUPPORTED_PLATFORMS = ["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "linux/s390x",
                       "linux/ppc64le"]


@dataclass
class ImageBuildConfiguration:
    scenario: BuildScenario
    version: str
    latest_tag: bool
    olm_tag: bool
    registries: List[str]
    dockerfile_path: str
    builder: str
    platforms: Optional[List[str]] = None
    sign: bool = False
    skip_if_exists: bool = False

    # Agent specific
    parallel: bool = False
    parallel_factor: int = 0
    all_agents: bool = False
    currently_used_agents: bool = False
    architecture_suffix: bool = False

    def is_release_scenario(self) -> bool:
        return self.scenario == BuildScenario.RELEASE

    def image_name(self) -> str:
        return self.registries[0].rpartition("/")[2]

    def get_registries(self) -> List[str]:
        """Return list of registries."""
        return self.registries
