"""Pure dataclasses describing the snapshot of state that ``display`` renders.

No subprocess, no IO. Domain modules build these and hand them off.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional


@dataclass
class GitState:
    branch: str
    clean: bool
    ahead: int
    behind: int
    upstream: Optional[str]


@dataclass
class DevcState:
    """Devcontainer compose stack."""

    project: str
    state: str  # running | exited | -
    services_running: int
    services_total: int
    image_age: Optional[str]  # human "2h" | None


@dataclass
class NetEntry:
    """One row from dc_select_network.sh --list / registry."""

    branch_dir: str
    prefix: int
    status: str  # active | stale
    worktree_path: Optional[str]


@dataclass
class NetState:
    prefix: Optional[int]  # this worktree's prefix (None when not allocated)
    subnet: Optional[str]
    namespace: Optional[str]
    gost_proxy: Optional[str]
    registered: bool
    orphans: int  # count of stale registry entries elsewhere


@dataclass
class EvgHostState:
    name: Optional[str]
    id: Optional[str]
    status: Optional[str]  # running | terminated | terminating | …
    host_name: Optional[str]  # ec2-… DNS
    expires_in: Optional[str]  # human "3h12m" | None
    ssh: Optional[str]
    # Parallel to expires_in so hint logic needn't re-parse the human string.
    # Negative when already expired; None when unknown.
    expires_seconds: Optional[int] = None


@dataclass
class KubeconfigState:
    path: Optional[str]
    last_patch: Optional[str]  # human age
    kfp_registered: Optional[bool]


@dataclass
class KfpHostState:
    """On-host kube-forwarding-proxy daemon snapshot.

    PID is owned by launchd and not surfaced here; only liveness signals.
    """

    listening: bool
    health: Optional[str]  # "ok" when /healthz is happy
    http_endpoint: str  # "127.0.0.1:11616"


@dataclass
class OmState:
    namespace: Optional[str]
    project_count: Optional[int]
    scope: Optional[str]


@dataclass
class WorktreeStatus:
    """Everything `wt-ctl status` (current worktree) renders."""

    worktree_dir: str
    worktree_path: str
    git: GitState
    context: Optional[str]
    devc: Optional[DevcState]
    network: NetState
    evg: Optional[EvgHostState]
    kubeconfig: Optional[KubeconfigState]
    om: Optional[OmState]
    kfp: Optional[KfpHostState] = None
    next_hints: list[str] = field(default_factory=list)


@dataclass
class WorktreeRow:
    """One line in `status --all`."""

    worktree: str
    branch: str
    prefix: Optional[int]
    namespace: Optional[str]
    devc_state: str
    evg_name: Optional[str]
    evg_status: Optional[str]
    evg_expires: Optional[str]
    prunable: bool = False
    prunable_reason: Optional[str] = None
    delete_cmd: Optional[str] = None


@dataclass
class OrphanRegistration:
    branch_dir: str
    prefix: int
    release_cmd: str


@dataclass
class GlobalStatus:
    rows: list[WorktreeRow]
    orphans: list[OrphanRegistration]
