from dataclasses import dataclass
from typing import List, Optional

from .build_context import BuildScenario


@dataclass
class BuildConfiguration:
    scenario: BuildScenario
    version: str
    base_registry: str

    parallel: bool = False
    parallel_factor: int = 0
    platforms: Optional[List[str]] = None
    sign: bool = False
    all_agents: bool = False

    def is_release_step_executed(self) -> bool:
        return self.scenario == BuildScenario.RELEASE
