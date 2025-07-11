from __future__ import annotations

from enum import Enum


class Phase(Enum):
    Running = 1
    Pending = 2
    Failed = 3
    Updated = 4
    Disabled = 5
    Unsupported = 6
