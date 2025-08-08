from dataclasses import dataclass
from typing import List, Optional

from scripts.release.build_context import BuildScenario

SUPPORTED_PLATFORMS = ["linux/amd64", "linux/arm64"]


@dataclass
class ImageBuildConfiguration:
    scenario: BuildScenario
    version: str
    registry: str

    parallel: bool = False
    parallel_factor: int = 0
    platforms: Optional[List[str]] = None
    sign: bool = False
    all_agents: bool = False

    def is_release_scenario(self) -> bool:
        return self.scenario == BuildScenario.RELEASE

    def image_name(self) -> str:
        return self.registry.split('/')[-1]
