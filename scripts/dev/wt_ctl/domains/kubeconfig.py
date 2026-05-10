"""Kubeconfig age + kfp-registration summary.

The plan only asks for a "fresh / last_patch=12m_ago / kfp_registered=yes"
style summary in `wt-ctl status`; we don't need to parse the actual yaml.
"""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

from ..runner import Runner
from ..state import KubeconfigState


def _age(path: Path) -> Optional[str]:
    if not path.is_file():
        return None
    try:
        mtime = datetime.fromtimestamp(path.stat().st_mtime, tz=timezone.utc)
    except OSError:
        return None
    delta = datetime.now(timezone.utc) - mtime
    secs = int(delta.total_seconds())
    if secs < 60:
        return f"{secs}s_ago"
    if secs < 3600:
        return f"{secs // 60}m_ago"
    if secs < 86400:
        return f"{secs // 3600}h_ago"
    return f"{secs // 86400}d_ago"


class KubeconfigDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    def state_for(self, worktree_root: Path) -> Optional[KubeconfigState]:
        gen = worktree_root / ".generated"
        # Prefer the host-side file; fall back to the devc variant.
        host_kc = gen / "evg-host.kubeconfig"
        devc_kc = gen / "evg-host.devc.kubeconfig"
        path = host_kc if host_kc.is_file() else (devc_kc if devc_kc.is_file() else None)
        if path is None:
            return None
        last_patch = _age(path)
        # kfp registration is ephemeral; we don't probe it from the host side
        # in Phase 1 — flagging "yes" only when the devc variant exists is
        # the cheapest signal that get-kubeconfig has been run inside the
        # container at least once.
        kfp = devc_kc.is_file() if path else None
        return KubeconfigState(
            path=str(path),
            last_patch=last_patch,
            kfp_registered=kfp,
        )
