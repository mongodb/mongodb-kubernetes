"""State file for the ``wt-ctl create`` orchestrator.

Persists per-phase status + input hash to
``<worktree>/.generated/wt-ctl/state.json`` so ``--resume`` and
``--restart-from`` can pick up after a failure. Read/write is atomic via
``os.replace`` on a tempfile sibling.

No subprocess. No domain imports.
"""

from __future__ import annotations

import hashlib
import json
import os
import tempfile
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

from .errors import StateConflict, WtCtlError
from .paths import state_dir


SCHEMA_VERSION = 1


# Phase status values: "pending" | "in_progress" | "ok" | "failed".
PENDING = "pending"
IN_PROGRESS = "in_progress"
OK = "ok"
FAILED = "failed"


# Canonical phase order for ``wt-ctl create``. Kept in this module so both
# orchestrator.py and state.py share the same source of truth.
PHASE_ORDER = (
    "worktree_init",
    "initialize_hook",
    "net_allocate",
    "evg_prepare",
    "dc_build",
    "dc_up",
    "reconcile",
    "post_start",
    "kubeconfig",
    "prepare_e2e",
)

# Phases that run in parallel as a logical group. Recorded individually
# (per the plan) but launched together.
PARALLEL_GROUP = ("evg_prepare", "dc_build")


@dataclass
class PhaseRecord:
    status: str = PENDING
    ts: Optional[str] = None
    input_hash: Optional[str] = None
    log: Optional[str] = None  # path relative to worktree_root, when known

    def to_dict(self) -> dict:
        return asdict(self)

    @classmethod
    def from_dict(cls, d: dict) -> "PhaseRecord":
        return cls(
            status=d.get("status", PENDING),
            ts=d.get("ts"),
            input_hash=d.get("input_hash"),
            log=d.get("log"),
        )


@dataclass
class OrchestratorState:
    """In-memory mirror of state.json. Loadable, mutable, atomically savable."""

    branch: str = ""
    started_at: Optional[str] = None
    updated_at: Optional[str] = None
    inputs: dict = field(default_factory=dict)
    phases: dict[str, PhaseRecord] = field(default_factory=dict)
    schema: int = SCHEMA_VERSION

    # ------------------------------------------------------------------
    # serialization
    # ------------------------------------------------------------------
    def to_dict(self) -> dict:
        return {
            "schema": self.schema,
            "branch": self.branch,
            "started_at": self.started_at,
            "updated_at": self.updated_at,
            "inputs": dict(self.inputs),
            "phases": {k: v.to_dict() for k, v in self.phases.items()},
        }

    @classmethod
    def from_dict(cls, data: dict) -> "OrchestratorState":
        return cls(
            schema=int(data.get("schema", SCHEMA_VERSION)),
            branch=data.get("branch", ""),
            started_at=data.get("started_at"),
            updated_at=data.get("updated_at"),
            inputs=dict(data.get("inputs") or {}),
            phases={
                name: PhaseRecord.from_dict(rec)
                for name, rec in (data.get("phases") or {}).items()
            },
        )

    # ------------------------------------------------------------------
    # builders / mutators
    # ------------------------------------------------------------------
    @classmethod
    def initial(cls, *, branch: str, inputs: dict, phase_order=PHASE_ORDER) -> "OrchestratorState":
        now = _now_iso()
        st = cls(
            branch=branch,
            started_at=now,
            updated_at=now,
            inputs=dict(inputs),
            phases={name: PhaseRecord(status=PENDING) for name in phase_order},
        )
        return st

    def ensure_phase(self, name: str) -> PhaseRecord:
        rec = self.phases.get(name)
        if rec is None:
            rec = PhaseRecord(status=PENDING)
            self.phases[name] = rec
        return rec

    def set_status(
        self,
        name: str,
        status: str,
        *,
        input_hash: Optional[str] = None,
        log: Optional[str] = None,
    ) -> None:
        rec = self.ensure_phase(name)
        rec.status = status
        rec.ts = _now_iso()
        if input_hash is not None:
            rec.input_hash = input_hash
        if log is not None:
            rec.log = log
        self.updated_at = rec.ts

    def has_in_progress(self) -> Optional[str]:
        for name, rec in self.phases.items():
            if rec.status == IN_PROGRESS:
                return name
        return None

    def first_non_ok(self, phase_order=PHASE_ORDER) -> Optional[str]:
        """Return the first phase whose status is not OK (used by --resume).
        ``input_hash`` mismatch is the caller's responsibility — this method
        returns purely on stored status.
        """
        for name in phase_order:
            rec = self.phases.get(name)
            if rec is None or rec.status != OK:
                return name
        return None

    def clear_from(self, phase: str, phase_order=PHASE_ORDER) -> None:
        """Reset ``phase`` and every later phase to PENDING (used by
        --restart-from). Phases not in ``phase_order`` are left alone.
        """
        if phase not in phase_order:
            raise StateConflict(f"unknown phase: {phase}")
        idx = phase_order.index(phase)
        for name in phase_order[idx:]:
            rec = self.ensure_phase(name)
            rec.status = PENDING
            rec.ts = None
            rec.input_hash = None
            rec.log = None
        self.updated_at = _now_iso()


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

def _now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def state_path(worktree_root: Path) -> Path:
    return state_dir(worktree_root) / "state.json"


def load(worktree_root: Path) -> Optional[OrchestratorState]:
    """Return None when no state.json exists yet."""
    path = state_path(worktree_root)
    if not path.is_file():
        return None
    try:
        text = path.read_text()
    except OSError as exc:
        raise StateConflict(f"failed to read {path}: {exc}") from exc
    try:
        data = json.loads(text or "{}")
    except json.JSONDecodeError as exc:
        raise StateConflict(f"state file at {path} is not valid JSON: {exc}") from exc
    if not isinstance(data, dict):
        raise StateConflict(f"state file at {path} must be a JSON object")
    return OrchestratorState.from_dict(data)


def save(worktree_root: Path, state: OrchestratorState) -> Path:
    """Atomically persist the state to
    ``<worktree>/.generated/wt-ctl/state.json``."""
    state.updated_at = _now_iso()
    path = state_path(worktree_root)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix="state.", suffix=".json", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w") as fh:
            json.dump(state.to_dict(), fh, indent=2, sort_keys=True)
            fh.write("\n")
        os.replace(tmp, str(path))
    except OSError as exc:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise StateConflict(f"failed to write {path}: {exc}") from exc
    return path


# ---------------------------------------------------------------------------
# input-hash helpers
# ---------------------------------------------------------------------------

def hash_inputs(payload: dict) -> str:
    """SHA256 of a json-canonicalized dict, first 16 hex chars (collision
    risk is irrelevant here — this is a "did inputs change?" guard).
    """
    canon = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canon.encode("utf-8")).hexdigest()[:16]
