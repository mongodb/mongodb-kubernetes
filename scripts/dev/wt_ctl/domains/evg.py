"""Evergreen host domain.

We rely on the ``evergreen`` CLI directly (the upstream JSON is the cleanest
contract) and fall back to wrapping ``scripts/dev/evg_host.sh`` for the
kubeconfig flow that already encapsulates yq + kfp registration.
"""

from __future__ import annotations

import json
import os
import re
import time
from pathlib import Path
from typing import Callable, Optional

from ..errors import ExternalCommandFailed, ToolMissing, WtCtlError
from ..runner import Runner
from ..state import EvgHostState


# Statuses we treat as "dead" for resume detection — same predicate as the
# old ``evg_prepare.sh`` (post-commit 4fce8275a).
_DEAD_STATUSES = frozenset({
    "terminated", "decommissioned", "quarantined", "failed",
})

# Default spawn parameters; match the defaults the legacy shell flow used.
_DEFAULT_DISTRO = "ubuntu2204-latest-large"
_DEFAULT_REGION = "eu-west-1"

# Heuristic to spot an AWS-assigned instance id (vs. an interim ``evg-*``
# placeholder).
_RE_AWS_ID = re.compile(r"^i-[0-9a-fA-F]+$")

# Best-effort host-id parse from ``evergreen host create`` stdout. Output
# format isn't documented; both ``Host i-deadbeef ...`` and bare-id lines
# have been observed across server versions.
_RE_CREATE_HOST_ID = re.compile(r"\b(i-[0-9a-fA-F]+|evg-[0-9a-zA-Z\-]+)\b")


class EvgDomain:
    def __init__(self, runner: Runner, repo_root: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root
        # evg_host.sh derives its kubeconfig path from PROJECT_DIR; we pass
        # the worktree as PROJECT_DIR when invoking the script.

    # ------------------------------------------------------------------
    def list_hosts(self, *, mine: bool = True) -> list[dict]:
        if not self.runner.have("evergreen"):
            raise ToolMissing("evergreen", hint="https://spruce.mongodb.com/preferences/cli")
        argv = ["evergreen", "host", "list", "--json"]
        if mine:
            argv.insert(-1, "--mine")
        res = self.runner.run(argv, check=False)
        if res.rc != 0:
            return []
        try:
            data = json.loads(res.stdout or "[]")
        except json.JSONDecodeError:
            return []
        return data if isinstance(data, list) else []

    def find_by_name(self, name: str) -> Optional[dict]:
        if not name:
            return None
        for h in self.list_hosts(mine=True):
            if h.get("name") == name:
                return h
        # fall back to global list (some setups don't tag hosts as --mine
        # consistently) — the project is shared anyway.
        for h in self.list_hosts(mine=False):
            if h.get("name") == name:
                return h
        return None

    # ------------------------------------------------------------------
    def state_for(self, worktree_root: Path) -> Optional[EvgHostState]:
        """Resolve the EVG host pinned to this worktree.

        Pin source order:
          1. ``.generated/.current-evg-host`` (set by evg_prepare.sh).
          2. By convention the EVG host display name == ``branch_dir`` —
             we fall back to that if no pin file exists.
        """
        pin = worktree_root / ".generated" / ".current-evg-host"
        if pin.is_file():
            name = pin.read_text().strip()
        else:
            name = worktree_root.name
        if not name:
            return None
        try:
            host = self.find_by_name(name)
        except ToolMissing:
            return EvgHostState(
                name=name, id=None, status=None,
                host_name=None, expires_in=None, ssh=None,
            )
        if host is None:
            return EvgHostState(
                name=name, id=None, status="not-found",
                host_name=None, expires_in=None, ssh=None,
            )
        host_name = host.get("host_name") or None
        user = host.get("user") or "ubuntu"
        ssh = f"ssh {user}@{host_name}" if host_name else None
        return EvgHostState(
            name=host.get("name"),
            id=host.get("id"),
            status=host.get("status"),
            host_name=host_name,
            expires_in=None,  # the --mine JSON doesn't include expiration
            ssh=ssh,
        )

    # ------------------------------------------------------------------
    def terminate(self, host_id: str) -> str:
        return self.runner.run(
            ["evergreen", "host", "terminate", "--host", host_id],
        ).stdout

    def extend(self, host_id: str, hours: int) -> str:
        return self.runner.run(
            ["evergreen", "host", "modify", "--extend-by", str(hours), "--host", host_id],
        ).stdout

    # ------------------------------------------------------------------
    def spawn(
        self,
        *,
        name: str,
        distro: str = _DEFAULT_DISTRO,
        region: str = _DEFAULT_REGION,
        key_name: Optional[str] = None,
        poll_interval_s: float = 5.0,
        timeout_s: float = 600.0,
        emit: Optional[Callable[[str], None]] = None,
    ) -> str:
        """Spawn (or resume) an EVG host with the given display name.

        Idempotency contract: if a host with ``displayName == name`` and
        ``status NOT IN {terminated, decommissioned, quarantined, failed}``
        already exists, resume it (no new spawn). Otherwise call
        ``evergreen host create`` once, then poll ``evergreen host list``
        until the spawned host reaches ``status == "running"``. Once AWS
        assigns an ``i-*`` id we best-effort tag + rename it to ``name``.
        Finally we attempt an SSH probe (warn-only — does not raise).

        Returns the final host id. Raises ``WtCtlError`` on terminal status
        in the poll loop or on timeout.

        Note: the upstream ``evergreen`` CLI has no ``--expiration`` flag;
        hosts inherit the server default. ``wt-ctl delete`` reaps them.
        """
        if not self.runner.have("evergreen"):
            raise ToolMissing("evergreen", hint="https://spruce.mongodb.com/preferences/cli")

        def _emit(msg: str) -> None:
            if emit is not None:
                emit(msg)

        # 1. Resume detection.
        existing = self._find_live_by_display_name(name)
        if existing is not None:
            host_id = existing.get("id") or ""
            status = (existing.get("status") or "").lower()
            _emit(
                f"[wt-ctl evg spawn] resume: existing host {host_id} "
                f"displayName={name!r} status={status!r}"
            )
            if status == "running":
                self._ssh_verify(existing, emit=_emit)
                return host_id
            # Fall through to the poll loop with the existing id.
            return self._poll_until_running(
                host_id=host_id,
                name=name,
                poll_interval_s=poll_interval_s,
                timeout_s=timeout_s,
                emit=_emit,
            )

        # 2. Fresh spawn.
        resolved_key = key_name or self._resolve_key_name()
        if not resolved_key:
            raise WtCtlError(
                "evg spawn: cannot resolve a public-key name. Set "
                "MCK_DEVC_EVG_KEY_NAME or pass --key (see "
                "'evergreen keys list --json')."
            )
        _emit(
            f"[wt-ctl evg spawn] spawning: distro={distro} region={region} "
            f"key={resolved_key} displayName={name!r}"
        )
        create_res = self.runner.run(
            [
                "evergreen", "host", "create",
                "--distro", distro,
                "--key", resolved_key,
                "--region", region,
            ],
            check=False,
        )
        if create_res.rc != 0:
            raise ExternalCommandFailed(
                argv=list(create_res.argv),
                rc=create_res.rc,
                stdout=create_res.stdout,
                stderr=create_res.stderr,
            )
        host_id = self._parse_host_id_from_create(create_res.stdout)
        if host_id is None:
            # Fallback: take the freshest --mine host with no displayName.
            host_id = self._find_freshest_untagged_host_id()
        if host_id is None:
            raise WtCtlError(
                "evg spawn: 'host create' succeeded but the host id "
                "could not be determined (parse fallback also empty). "
                f"create stdout:\n{create_res.stdout}"
            )
        _emit(f"[wt-ctl evg spawn] new host id={host_id}")
        return self._poll_until_running(
            host_id=host_id,
            name=name,
            poll_interval_s=poll_interval_s,
            timeout_s=timeout_s,
            emit=_emit,
        )

    # ------------------------------------------------------------------
    # spawn helpers
    # ------------------------------------------------------------------
    def _list_my_hosts_json(self) -> list[dict]:
        """``evergreen host list --mine --json`` — raises on non-zero (vs.
        ``list_hosts`` which swallows errors for status-rendering paths).
        """
        res = self.runner.run(
            ["evergreen", "host", "list", "--mine", "--json"],
            check=False,
        )
        if res.rc != 0:
            raise ExternalCommandFailed(
                argv=list(res.argv), rc=res.rc,
                stdout=res.stdout, stderr=res.stderr,
            )
        try:
            data = json.loads(res.stdout or "[]")
        except json.JSONDecodeError:
            return []
        return data if isinstance(data, list) else []

    def _find_live_by_display_name(self, name: str) -> Optional[dict]:
        """Return the first --mine host with displayName==name in a non-dead
        status; None if no such host exists.
        """
        for h in self._list_my_hosts_json():
            display = h.get("display_name") or h.get("displayName") or h.get("name")
            if display != name:
                continue
            status = (h.get("status") or "").lower()
            if status in _DEAD_STATUSES:
                continue
            return h
        return None

    def _find_host_by_id(self, hosts: list[dict], host_id: str) -> Optional[dict]:
        for h in hosts:
            if h.get("id") == host_id:
                return h
        return None

    def _find_freshest_untagged_host_id(self) -> Optional[str]:
        """Fallback when ``host create`` stdout doesn't yield a parseable id.

        Pick the --mine host with the most recent ``creation_time`` whose
        ``displayName`` is empty (i.e. ours; we haven't renamed it yet).
        """
        candidates: list[tuple[str, str]] = []
        for h in self._list_my_hosts_json():
            display = h.get("display_name") or h.get("displayName") or h.get("name") or ""
            if display:
                continue
            hid = h.get("id") or ""
            ctime = h.get("creation_time") or h.get("creationTime") or ""
            if hid:
                candidates.append((ctime, hid))
        if not candidates:
            return None
        candidates.sort(reverse=True)
        return candidates[0][1]

    def _resolve_key_name(self) -> str:
        """Return the EVG-managed public-key *name* to pass to ``--key``.

        Order: ``MCK_DEVC_EVG_KEY_NAME`` env var → first key from
        ``evergreen keys list --json``.
        """
        env_name = os.environ.get("MCK_DEVC_EVG_KEY_NAME", "").strip()
        if env_name:
            return env_name
        res = self.runner.run(
            ["evergreen", "keys", "list", "--json"], check=False,
        )
        if res.rc != 0:
            return ""
        try:
            data = json.loads(res.stdout or "[]")
        except json.JSONDecodeError:
            return ""
        if not isinstance(data, list) or not data:
            return ""
        first = data[0]
        if isinstance(first, dict):
            return first.get("name") or ""
        return ""

    def _parse_host_id_from_create(self, stdout: str) -> Optional[str]:
        """Best-effort host-id parse from ``evergreen host create`` stdout."""
        if not stdout:
            return None
        # Prefer an i-* id (final AWS instance); otherwise an evg-* placeholder.
        i_match = re.search(r"\bi-[0-9a-fA-F]+\b", stdout)
        if i_match:
            return i_match.group(0)
        match = _RE_CREATE_HOST_ID.search(stdout)
        return match.group(0) if match else None

    def _poll_until_running(
        self,
        *,
        host_id: str,
        name: str,
        poll_interval_s: float,
        timeout_s: float,
        emit: Callable[[str], None],
    ) -> str:
        """Poll ``host list`` until our host reaches status=running.

        Side-effects: once AWS issues an ``i-*`` id (vs. the interim
        ``evg-*``) we best-effort issue ``host modify --name --tag`` exactly
        once. Failures of the modify step are swallowed (warn-only) — the
        spawn itself isn't compromised by a missing display name.

        Returns the (possibly-updated) host id when status=running; raises
        ``WtCtlError`` on terminal status or timeout.
        """
        deadline = time.monotonic() + timeout_s
        tagged = False
        last_status: Optional[str] = None
        current_id = host_id
        while True:
            hosts = self._list_my_hosts_json()
            host = self._find_host_by_id(hosts, current_id)
            if host is None:
                # Sometimes the host id we have is an ``evg-*`` placeholder
                # that flips to ``i-*``; try matching by name as a fallback.
                host = self._find_live_by_display_name(name)
                if host is not None and host.get("id"):
                    new_id = host["id"]
                    if new_id != current_id:
                        emit(
                            f"[wt-ctl evg spawn] host id transitioned: "
                            f"{current_id} -> {new_id}"
                        )
                        current_id = new_id
            status = ((host or {}).get("status") or "").lower()
            if status != last_status:
                emit(f"[wt-ctl evg spawn] host {current_id} status={status!r}")
                last_status = status
            if status in _DEAD_STATUSES:
                raise WtCtlError(
                    f"evg spawn: host {current_id} entered terminal "
                    f"status={status!r} before reaching 'running'"
                )
            # Tag + rename once we have a real AWS id and we haven't yet.
            if not tagged and host is not None and _RE_AWS_ID.match(current_id):
                tag_res = self.runner.run(
                    [
                        "evergreen", "host", "modify",
                        "--host", current_id,
                        "--name", name,
                        "--tag", f"name={name}",
                    ],
                    check=False,
                )
                if tag_res.rc == 0:
                    emit(
                        f"[wt-ctl evg spawn] tagged + renamed {current_id} "
                        f"displayName={name!r}"
                    )
                    tagged = True
                else:
                    emit(
                        f"[wt-ctl evg spawn] WARN: tag/rename failed for "
                        f"{current_id} (rc={tag_res.rc}); will retry on next poll"
                    )
            if status == "running":
                if host is not None:
                    self._ssh_verify(host, emit=emit)
                return current_id
            if time.monotonic() >= deadline:
                raise WtCtlError(
                    f"evg spawn: timed out after {timeout_s:g}s waiting for "
                    f"host {current_id} to reach 'running' (last status="
                    f"{status!r})"
                )
            time.sleep(poll_interval_s)

    def _ssh_verify(self, host: dict, *, emit: Callable[[str], None]) -> bool:
        """Best-effort SSH probe to ``ubuntu@<host.host_name>``.

        Uses the canonical evg-host key from ``~/.ssh`` (per memory
        ``feedback_evg_host_ssh_keys``); never ssh-keyscans. Logs and
        returns False on failure — the spawn return value still wins.
        """
        host_name = (
            host.get("host_name") or host.get("dns_name") or host.get("dnsName")
        )
        if not host_name:
            emit("[wt-ctl evg spawn] SSH verify: skipped (no host_name in JSON)")
            return False
        user = host.get("user") or "ubuntu"
        ssh_key = Path.home() / ".ssh" / "evg-host"
        argv = [
            "ssh",
            "-i", str(ssh_key),
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=5",
            "-o", "StrictHostKeyChecking=accept-new",
            f"{user}@{host_name}",
            "true",
        ]
        res = self.runner.run(argv, check=False)
        if res.rc == 0:
            emit(f"[wt-ctl evg spawn] SSH verify ok: {user}@{host_name}")
            return True
        emit(
            f"[wt-ctl evg spawn] WARN: SSH verify failed (rc={res.rc}) "
            f"for {user}@{host_name}; spawn returns host id anyway"
        )
        return False

    # ------------------------------------------------------------------
    def get_kubeconfig(self, worktree_root: Path, *, no_fetch: bool = False) -> str:
        """Wrap ``scripts/dev/evg_host.sh get-kubeconfig`` (so all the yq +
        kfp PATCH logic stays in one place — the workhorse).
        """
        env = self._evg_env(worktree_root)
        if env is None:
            return "(no EVG_HOST_NAME pinned; skipping)"
        argv = [
            str(self.repo_root / "scripts/dev/evg_host.sh"),
            "get-kubeconfig",
        ]
        if no_fetch:
            argv.append("--no-fetch")
        res = self.runner.run(argv, env=env, cwd=worktree_root)
        return res.stdout

    # ------------------------------------------------------------------
    def _evg_env(self, worktree_root: Path) -> Optional[dict]:
        """Build env for evg_host.sh: PROJECT_DIR + EVG_HOST_NAME."""
        host_pin = worktree_root / ".generated" / ".current-evg-host"
        host_name = host_pin.read_text().strip() if host_pin.is_file() else worktree_root.name
        if not host_name:
            return None
        return {
            "PROJECT_DIR": str(worktree_root),
            "EVG_HOST_NAME": host_name,
        }
