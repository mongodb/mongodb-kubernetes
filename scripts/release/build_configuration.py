from typing import Optional, List
from dataclasses import dataclass


@dataclass
class BuildConfiguration:
    image_type: str
    base_registry: str

    parallel: bool = False
    parallel_factor: int = 0
    architecture: Optional[List[str]] = None
    sign: bool = False
    all_agents: bool = False

    pipeline: bool = True
    debug: bool = True
