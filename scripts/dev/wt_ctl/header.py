"""The first line of every verb, on stderr so stdout stays parseable."""

from __future__ import annotations

import sys
from typing import Optional

from .paths import WorktreeRefs


def emit_banner(refs: Optional[WorktreeRefs], *, quiet: bool = False) -> None:
    if quiet or refs is None:
        return
    sys.stderr.write(f"[wt-ctl] worktree={refs.branch_dir}  branch={refs.branch}\n")
