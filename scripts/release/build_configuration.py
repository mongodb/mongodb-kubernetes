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
    debug: bool = True

    def is_release_step_executed(self) -> bool:
        """
        Determines whether release steps should be executed based on build scenario
        """
        return self.scenario == BuildScenario.RELEASE
