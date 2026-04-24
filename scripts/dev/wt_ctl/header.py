"""First-line banner. Always written to **stderr** so stdout is parseable."""

from __future__ import annotations

import sys
from typing import Optional

from .paths import WorktreeRefs


def emit_banner(refs: Optional[WorktreeRefs], *, quiet: bool = False) -> None:
    """Print ``[wt-ctl] worktree=<dir>  branch=<branch>`` to stderr.

    Skipped entirely when ``quiet`` or when ``refs is None`` (verbs that
    work outside a worktree, e.g. ``status --all``).
    """
    if quiet or refs is None:
        return
    sys.stderr.write(f"[wt-ctl] worktree={refs.branch_dir}  branch={refs.branch}\n")
    sys.stderr.flush()
