"""Evergreen host domain.

We rely on the ``evergreen`` CLI directly (the upstream JSON is the cleanest
contract) and fall back to wrapping ``scripts/dev/evg_host.sh`` for the
kubeconfig flow that already encapsulates yq + kfp registration.
"""

from __future__ import annotations

import contextlib
import fcntl
import json
import os
import re
import tempfile
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Iterator, Optional

from ..errors import ExternalCommandFailed, ToolMissing, WtCtlError
from ..runner import Runner
from ..state import EvgHostState

# Best-effort REST API probe for expiration_time (the --mine JSON omits it).
# Read on demand; keep short to avoid blocking status rendering.
_EVG_API_TIMEOUT_S = 3.0
_EVG_CONFIG_PATH = Path.home() / ".evergreen.yml"


# Statuses we treat as "dead" for resume detection.
_DEAD_STATUSES = frozenset(
    {
        "terminated",
        "decommissioned",
        "quarantined",
        "failed",
    }
)

# Default spawn parameters.
_DEFAULT_DISTRO = "ubuntu2204-latest-large"
_DEFAULT_REGION = "eu-west-1"

# Heuristic to spot an AWS-assigned instance id (vs. an interim ``evg-*``
# placeholder).
_RE_AWS_ID = re.compile(r"^i-[0-9a-fA-F]+$")

# Best-effort host-id parse from ``evergreen host create`` stdout. Output
# format isn't documented; both ``Host i-deadbeef ...`` and bare-id lines
# have been observed across server versions.
_RE_CREATE_HOST_ID = re.compile(r"\b(i-[0-9a-fA-F]+|evg-[0-9a-zA-Z\-]+)\b")


def _tail_diag(text: Optional[str], *, lines: int = 1) -> str:
    """Last non-empty line(s) of a command's stderr/stdout, for a terse
    one-line diagnostic in spawn-retry logs."""
    non_empty = [ln.strip() for ln in (text or "").splitlines() if ln.strip()]
    return " | ".join(non_empty[-lines:]) if non_empty else ""


class EvgDomain:
    def __init__(self, runner: Runner, repo_root: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root

    # ------------------------------------------------------------------
    def list_hosts(self, *, mine: bool = True) -> list[dict]:
        """Non-raising host list for status-rendering AND the delete/terminate
        path. Retries transient CLI flakes (rc!=0 / banner-corrupted JSON — see
        _host_list_with_retry) so teardown doesn't silently skip a real host and
        leak it; returns [] only after retries are genuinely exhausted.
        """
        if not self.runner.have("evergreen"):
            raise ToolMissing("evergreen", hint="https://spruce.mongodb.com/preferences/cli")
        data = self._host_list_with_retry(mine=mine)
        return data if isinstance(data, list) else []

    def find_by_name(self, name: str, *, mine_only: bool = False) -> Optional[dict]:
        if not name:
            return None
        for h in self.list_hosts(mine=True):
            if h.get("name") == name:
                return h
        # The global-list fallback is unsafe for destructive verbs: a display-name
        # collision could resolve a teammate's host. Callers that terminate/extend
        # pass mine_only=True.
        if mine_only:
            return None
        for h in self.list_hosts(mine=False):
            if h.get("name") == name:
                return h
        return None

    # ------------------------------------------------------------------
    def state_for(self, worktree_root: Path, *, mine_only: bool = False) -> Optional[EvgHostState]:
        """Resolve the EVG host pinned to this worktree.

        Pin source order:
          1. ``.generated/.current-evg-host`` (set by evg_prepare.sh).
          2. By convention the EVG host display name == ``branch_dir`` —
             we fall back to that if no pin file exists.

        ``mine_only=True`` refuses the global-list fallback — required for
        destructive verbs so a name collision can't hit a teammate's host.
        """
        pin = worktree_root / ".generated" / ".current-evg-host"
        if pin.is_file():
            name = pin.read_text().strip()
        else:
            name = worktree_root.name
        if not name:
            return None
        try:
            host = self.find_by_name(name, mine_only=mine_only)
        except ToolMissing:
            return EvgHostState(
                name=name,
                id=None,
                status=None,
                host_name=None,
                expires_in=None,
                ssh=None,
            )
        if host is None:
            return EvgHostState(
                name=name,
                id=None,
                status="not-found",
                host_name=None,
                expires_in=None,
                ssh=None,
            )
        host_name = host.get("host_name") or None
        user = host.get("user") or "ubuntu"
        ssh = f"ssh {user}@{host_name}" if host_name else None
        host_id = host.get("id")
        # --mine JSON omits expiration; fetch via REST (best-effort).
        expires_human, expires_secs = self.fetch_expires_in(host_id) if host_id else (None, None)
        return EvgHostState(
            name=host.get("name"),
            id=host_id,
            status=host.get("status"),
            host_name=host_name,
            expires_in=expires_human,
            ssh=ssh,
            expires_seconds=expires_secs,
        )

    # ------------------------------------------------------------------
    def terminate(self, host_id: str) -> str:
        return self.runner.run(
            ["evergreen", "host", "terminate", "--host", host_id],
        ).stdout

    def extend(self, host_id: str, hours: int) -> str:
        # Upstream CLI flag is ``--extend HOURS`` (not ``--extend-by``).
        return self.runner.run(
            ["evergreen", "host", "modify", "--extend", str(hours), "--host", host_id],
        ).stdout

    # ------------------------------------------------------------------
    def fetch_expires_in(self, host_id: str) -> tuple[Optional[str], Optional[int]]:
        """Return ``(human "Xh Ym", remaining_seconds)`` for ``host_id``,
        or ``(None, None)`` on any failure.

        The ``host list --json`` payload omits expiration, so we hit the
        REST API directly. Best-effort: missing config, network error, or
        parse failure all collapse to ``(None, None)`` and status rendering
        carries on.
        """
        if not host_id:
            return None, None
        expiration = _fetch_expiration_time(host_id)
        if expiration is None:
            return None, None
        secs = int((expiration - datetime.now(timezone.utc)).total_seconds())
        return _format_remaining_secs(secs), secs

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
        create_retries: int = 2,
        create_timeout_s: float = 180.0,
        reconcile_timeout_s: Optional[float] = None,
        resolve_timeout_s: float = 240.0,
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
            _emit(f"[wt-ctl evg spawn] resume: existing host {host_id} " f"displayName={name!r} status={status!r}")
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
        if key_name:
            resolved_key, resolve_diag = key_name, ""
        else:
            resolved_key, resolve_diag = self._resolve_key_name()
        if not resolved_key:
            raise WtCtlError(
                "evg spawn: cannot resolve a public-key name. Set "
                "MCK_DEVC_EVG_KEY_NAME or pass --key (see "
                "'evergreen keys list'). "
                f"Diagnostic: {resolve_diag or 'no key candidates'}"
            )
        _emit(
            f"[wt-ctl evg spawn] spawning: distro={distro} region={region} " f"key={resolved_key} displayName={name!r}"
        )
        # The `evergreen host create` placeholder id is deterministic in
        # (distro, timestamp-to-the-second), so parallel same-distro spawns
        # collide on one `evg-*` id and the snapshot-diff can't tell the real
        # `i-*` hosts apart, leaking nameless hosts. Serialize just the
        # create→resolve→name critical section with a host-local lock; the long
        # wait-for-running below stays unlocked so spawns still overlap.
        with self._spawn_lock(emit=_emit):
            # Snapshot --mine host ids before create: any id absent here but
            # present after is ours. Under the lock this set-diff yields exactly
            # one new host.
            try:
                pre_known_ids = {h.get("id") for h in self._list_my_hosts_json() if h.get("id")}
            except ExternalCommandFailed:
                pre_known_ids = set()
            # Tag at create (not just in the follow-up modify): the tag rides
            # the create request, so it survives a create whose POST times out
            # client-side but succeeds server-side — otherwise a nameless,
            # unattributable leaked host. `name` is reserved by the API, so use
            # the same `wt-ctl=<name>` side label the rename step applies.
            create_argv = [
                "evergreen",
                "host",
                "create",
                "--distro",
                distro,
                "--key",
                resolved_key,
                "--region",
                region,
                "--tag",
                f"wt-ctl={name}",
            ]
            # Reconcile-then-retry: a `host create` POST can time out client-
            # side while succeeding server-side. So we NEVER blindly re-create —
            # after each attempt (success, timeout, or error) we look for a host
            # that appeared since pre_known_ids and adopt it; only when nothing
            # appeared do we re-issue create. This makes retries leak-free.
            host_id: Optional[str] = None
            last_diag = ""
            attempts = max(1, create_retries + 1)
            for attempt in range(1, attempts + 1):
                timed_out = False
                try:
                    create_res = self.runner.run(create_argv, check=False, timeout=create_timeout_s)
                    if create_res.rc == 0:
                        host_id = self._parse_host_id_from_create(create_res.stdout)
                    else:
                        last_diag = _tail_diag(create_res.stderr or create_res.stdout)
                        _emit(
                            f"[wt-ctl evg spawn] 'host create' attempt {attempt}/{attempts} "
                            f"rc={create_res.rc}; reconciling ({last_diag})"
                        )
                except ExternalCommandFailed as exc:
                    # Client-side timeout / transport error: server may have made
                    # the host regardless — reconcile, don't re-create blindly.
                    timed_out = True
                    last_diag = _tail_diag(exc.stderr or exc.stdout)
                    _emit(
                        f"[wt-ctl evg spawn] 'host create' attempt {attempt}/{attempts} "
                        f"failed to return ({last_diag}); reconciling for a server-created host"
                    )
                if host_id is None:
                    # Adopt a host that appeared post-snapshot (covers a timed-
                    # out-but-succeeded create and an unparseable stdout). A
                    # timeout gets a generous window; a fast reject a short one.
                    deadline_s = (
                        reconcile_timeout_s if reconcile_timeout_s is not None else (90.0 if timed_out else 20.0)
                    )
                    host_id = self._await_new_host_id(
                        pre_known_ids,
                        emit=_emit,
                        deadline_s=deadline_s,
                        poll_interval_s=poll_interval_s,
                    )
                if host_id is not None:
                    break
                if attempt < attempts:
                    _emit(f"[wt-ctl evg spawn] no host appeared; retrying create ({attempt}/{attempts})")
            if host_id is None:
                raise WtCtlError(
                    f"evg spawn: 'host create' produced no host after {attempts} "
                    f"attempt(s) and no server-created host appeared. last diag: {last_diag or 'n/a'}"
                )
            _emit(f"[wt-ctl evg spawn] new host id={host_id}")
            # Resolve the interim `evg-*` placeholder to the real `i-*` id and
            # rename it, still under the lock. Once named + real-id'd it is no
            # longer an ambiguous nameless candidate for the next spawn.
            host_id = self._resolve_real_id(
                host_id,
                name,
                pre_known_ids,
                emit=_emit,
                poll_interval_s=poll_interval_s,
                timeout_s=resolve_timeout_s,
            )
        # Lock released: the host has a real id and display name. Wait for
        # 'running' unlocked so sibling spawns proceed in parallel.
        return self._poll_until_running(
            host_id=host_id,
            name=name,
            poll_interval_s=poll_interval_s,
            timeout_s=timeout_s,
            emit=_emit,
            pre_known_ids=pre_known_ids,
            already_tagged=True,
        )

    # ------------------------------------------------------------------
    # spawn helpers
    # ------------------------------------------------------------------
    def _list_my_hosts_json(self, *, retries: int = 4, retry_interval_s: float = 3.0) -> list[dict]:
        """``evergreen host list --mine --json`` — raises on non-zero (vs.
        ``list_hosts`` which swallows errors for status-rendering paths).

        Hardened against transient CLI flakes: the Evergreen client prepends a
        status banner (e.g. GitHub-API-slowness) to stdout, corrupting the JSON,
        and occasionally exits non-zero under that load. Both are retried; the
        banner is stripped before parsing. Raises only after retries are
        exhausted so a persistent failure still surfaces (never silently []).
        """
        data = self._host_list_with_retry(mine=True, retries=retries, retry_interval_s=retry_interval_s)
        if isinstance(data, list):
            return data
        raise ExternalCommandFailed(
            argv=list(data.argv),
            rc=data.rc,
            stdout=data.stdout,
            stderr=data.stderr,
        )

    def _host_list_with_retry(self, *, mine: bool, retries: int = 4, retry_interval_s: float = 3.0):
        """``host list [--mine] --json`` with retry + banner-strip. Returns the
        parsed list on success, or the last failed ``CmdResult`` sentinel when
        all attempts fail (rc!=0 or unparseable). Callers choose whether a
        persistent failure raises (``_list_my_hosts_json``) or degrades to []
        (``list_hosts``).
        """
        argv = ["evergreen", "host", "list", "--json"]
        if mine:
            argv.insert(-1, "--mine")
        last = None
        for attempt in range(retries + 1):
            res = self.runner.run(argv, check=False)
            if res.rc == 0:
                data = self._parse_hosts_json(res.stdout)
                if data is not None:
                    return data
            last = res
            if attempt < retries:
                time.sleep(retry_interval_s)
        return last

    @staticmethod
    def _parse_hosts_json(stdout: str) -> Optional[list[dict]]:
        """Extract the JSON array from ``host list --json`` stdout, tolerating a
        leading banner line. Returns None if no parseable array is present.
        """
        text = stdout or ""
        start = text.find("[")
        if start == -1:
            return None
        try:
            data = json.loads(text[start:])
        except json.JSONDecodeError:
            return None
        return data if isinstance(data, list) else None

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

    @contextlib.contextmanager
    def _spawn_lock(self, *, emit: Callable[[str], None]) -> Iterator[None]:
        """Serialize the create→resolve→name critical section across parallel
        wt-ctl processes via an advisory ``flock`` on a shared host-local file.

        The lock is process-wide (not per-name) on purpose: the `evg-*`
        placeholder collision is between ANY two concurrent same-distro
        creates, so all spawns must take turns naming their host. Overridable
        via ``MCK_DEVC_EVG_SPAWN_LOCK`` (mainly for tests / isolation).
        """
        lock_path = os.environ.get(
            "MCK_DEVC_EVG_SPAWN_LOCK",
            str(Path(tempfile.gettempdir()) / "wt-ctl-evg-spawn.lock"),
        )
        f = open(lock_path, "w")  # noqa: SIM115 — held for the with-body
        try:
            emit("[wt-ctl evg spawn] waiting for spawn lock (serialized id resolution)")
            fcntl.flock(f.fileno(), fcntl.LOCK_EX)
            emit("[wt-ctl evg spawn] acquired spawn lock")
            yield
        finally:
            with contextlib.suppress(OSError):
                fcntl.flock(f.fileno(), fcntl.LOCK_UN)
            f.close()

    def _resolve_real_id(
        self,
        host_id: str,
        name: str,
        pre_known_ids: set,
        *,
        emit: Callable[[str], None],
        poll_interval_s: float = 5.0,
        timeout_s: float = 240.0,
    ) -> str:
        """Poll until our host has a real AWS ``i-*`` id, then rename it to
        ``name``. Returns the ``i-*`` id. Must be called under ``_spawn_lock``
        so the "one nameless new host" invariant holds and the snapshot-diff is
        unambiguous. Raises ``WtCtlError`` on terminal status / timeout.
        """
        deadline = time.monotonic() + timeout_s
        while True:
            hosts = self._list_my_hosts_json()
            # Prefer a host already bearing our display name (idempotent
            # re-entry), else the single new nameless real-id host.
            host = next(
                (h for h in hosts if (h.get("display_name") or h.get("displayName") or h.get("name")) == name),
                None,
            )
            if host is None:
                new_real_nameless = [
                    h
                    for h in hosts
                    if h.get("id")
                    and h.get("id") not in pre_known_ids
                    and _RE_AWS_ID.match(h.get("id") or "")
                    and not (h.get("display_name") or h.get("displayName") or h.get("name"))
                ]
                if len(new_real_nameless) == 1:
                    host = new_real_nameless[0]
            if host is not None:
                status = (host.get("status") or "").lower()
                if status in _DEAD_STATUSES:
                    raise WtCtlError(
                        f"evg spawn: host {host.get('id')} entered terminal status={status!r} during id resolution"
                    )
                rid = host.get("id") or ""
                if _RE_AWS_ID.match(rid):
                    if rid != host_id:
                        emit(f"[wt-ctl evg spawn] host id resolved: {host_id} -> {rid}")
                    if self._tag_and_rename(rid, name):
                        emit(f"[wt-ctl evg spawn] named {rid} displayName={name!r}")
                    else:
                        emit(f"[wt-ctl evg spawn] WARN: rename failed for {rid}; poll loop will retry")
                    return rid
            if time.monotonic() >= deadline:
                raise WtCtlError(f"evg spawn: host {host_id} did not resolve to a real AWS id within {timeout_s:g}s")
            time.sleep(poll_interval_s)

    def _await_new_host_id(
        self,
        pre_known_ids: set,
        *,
        emit: Callable[[str], None],
        deadline_s: float = 90.0,
        poll_interval_s: float = 5.0,
    ) -> Optional[str]:
        """Poll ``host list`` for a non-dead host that appeared AFTER the
        pre-create snapshot and adopt it; return its id, or None by deadline.

        This is the anti-leak reconcile: a ``host create`` that times out
        client-side often still creates the host server-side. Rather than
        re-create (and leak), we discover the new host by id set-diff and adopt
        it. If MORE than one new host appears (a prior attempt already leaked),
        adopt the freshest and terminate the surplus so nothing is orphaned.
        """
        deadline = time.monotonic() + deadline_s
        while True:
            try:
                hosts = self._list_my_hosts_json()
            except ExternalCommandFailed:
                hosts = []
            new = [
                h
                for h in hosts
                if h.get("id")
                and h.get("id") not in pre_known_ids
                and (h.get("status") or "").lower() not in _DEAD_STATUSES
            ]
            if new:
                new.sort(
                    key=lambda h: h.get("creation_time") or h.get("creationTime") or "",
                    reverse=True,
                )
                adopted = new[0].get("id") or ""
                for extra in new[1:]:
                    eid = extra.get("id") or ""
                    emit(f"[wt-ctl evg spawn] WARN: surplus new host {eid}; terminating to avoid leak")
                    try:
                        self.terminate(eid)
                    except (ExternalCommandFailed, WtCtlError):
                        emit(f"[wt-ctl evg spawn] WARN: could not terminate surplus host {eid}")
                emit(f"[wt-ctl evg spawn] adopted server-created host {adopted}")
                return adopted
            if time.monotonic() >= deadline:
                return None
            time.sleep(poll_interval_s)

    def _resolve_key_name(self) -> tuple[str, str]:
        """Return the EVG-managed public-key *name* to pass to ``--key``.

        Order:
        1. ``MCK_DEVC_EVG_KEY_NAME`` env var.
        2. ``evg-host`` if the user has it registered (canonical per the
           project's SSH policy — the evg-host private key lives in
           ``~/.ssh/evg-host`` and ``evg_host.sh`` uses it for SSH probes).
        3. First key listed by ``evergreen keys list``.

        The upstream CLI has no ``--json`` flag for ``keys list``; output
        is plain text of the form ``Name: 'NAME', Key: 'TYPE KEYBLOB COMMENT'``.

        Returns ``(name, diag)`` where ``name`` is empty when resolution
        failed and ``diag`` is a short human-readable reason for the empty
        result — useful in the spawn-time error message because the
        upstream ``evergreen keys list`` itself can fail transiently
        (e.g. when several wt-ctl runs hit the API concurrently).
        """
        env_name = os.environ.get("MCK_DEVC_EVG_KEY_NAME", "").strip()
        if env_name:
            return env_name, ""
        res = self.runner.run(
            ["evergreen", "keys", "list"],
            check=False,
        )
        if res.rc != 0:
            tail_stderr = (res.stderr or "").strip().splitlines()[-3:]
            return "", (f"`evergreen keys list` failed rc={res.rc} " f"stderr_tail={tail_stderr!r}")
        names: list[str] = []
        for line in (res.stdout or "").splitlines():
            m = re.match(r"^\s*Name:\s*'([^']+)'", line)
            if m:
                names.append(m.group(1))
        if not names:
            head_stdout = (res.stdout or "").strip().splitlines()[:3]
            return "", (
                "`evergreen keys list` returned no parseable 'Name: ...' " f"entries; stdout_head={head_stdout!r}"
            )
        if "evg-host" in names:
            return "evg-host", ""
        return names[0], ""

    def _tag_and_rename(self, host_id: str, name: str) -> bool:
        """Set display name + ``wt-ctl`` label on ``host_id``; True on rc==0.

        Evergreen reserves the ``name`` tag key (400 from the API), so we
        ride a ``wt-ctl=<name>`` side label with the ``--name`` rename in a
        single call (``modify`` needs at least one tag/expire/type op).
        """
        res = self.runner.run(
            [
                "evergreen",
                "host",
                "modify",
                "--host",
                host_id,
                "--name",
                name,
                "--tag",
                f"wt-ctl={name}",
            ],
            check=False,
        )
        return res.rc == 0

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
        pre_known_ids: Optional[set[str]] = None,
        already_tagged: bool = False,
    ) -> str:
        """Poll ``host list`` until our host reaches status=running.

        Resolution is primarily by display name (set by ``_resolve_real_id``
        under the spawn lock); ``pre_known_ids`` is a fallback set-diff for
        when the name is still empty. ``already_tagged`` skips the in-loop
        rename retry when naming already succeeded; otherwise, once AWS issues
        an ``i-*`` id, we best-effort retry ``host modify --name --tag``
        (warn-only on failure). Returns the host id at status=running; raises
        ``WtCtlError`` on terminal status or timeout.
        """
        deadline = time.monotonic() + timeout_s
        tagged = already_tagged
        last_status: Optional[str] = None
        current_id = host_id
        if pre_known_ids is None:
            pre_known_ids = set()
        while True:
            hosts = self._list_my_hosts_json()
            host = self._find_host_by_id(hosts, current_id)
            if host is None:
                # Sometimes the host id we have is an ``evg-*`` placeholder
                # that flips to ``i-*``; try matching by name as a fallback.
                host = self._find_live_by_display_name(name)
                if host is None and not _RE_AWS_ID.match(current_id):
                    # Placeholder ``evg-*`` not in list and name not yet
                    # tagged → fall back to "any new id we didn't see before
                    # the create call". With concurrent spawns by sibling
                    # orchestrators the pre-snapshot diff is the only way to
                    # pick the right host.
                    new_candidates = [
                        h
                        for h in hosts
                        if h.get("id") and h.get("id") not in pre_known_ids and _RE_AWS_ID.match(h.get("id") or "")
                    ]
                    # Only flip the id when exactly one candidate is new —
                    # otherwise (zero or 2+) keep polling for clarity.
                    if len(new_candidates) == 1:
                        host = new_candidates[0]
                if host is not None and host.get("id"):
                    new_id = host["id"]
                    if new_id != current_id:
                        emit(f"[wt-ctl evg spawn] host id transitioned: " f"{current_id} -> {new_id}")
                        current_id = new_id
            status = ((host or {}).get("status") or "").lower()
            if status != last_status:
                emit(f"[wt-ctl evg spawn] host {current_id} status={status!r}")
                last_status = status
            if status in _DEAD_STATUSES:
                raise WtCtlError(
                    f"evg spawn: host {current_id} entered terminal " f"status={status!r} before reaching 'running'"
                )
            # Retry tag + rename if the early claim failed, once we have a
            # real AWS id.
            if not tagged and host is not None and _RE_AWS_ID.match(current_id):
                if self._tag_and_rename(current_id, name):
                    emit(f"[wt-ctl evg spawn] tagged + renamed {current_id} " f"displayName={name!r}")
                    tagged = True
                else:
                    emit(f"[wt-ctl evg spawn] WARN: tag/rename failed for " f"{current_id}; will retry on next poll")
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

        Uses the canonical evg-host key from ``~/.ssh``; never ssh-keyscans.
        Logs and returns False on failure — the spawn return value still wins.
        """
        host_name = host.get("host_name") or host.get("dns_name") or host.get("dnsName")
        if not host_name:
            emit("[wt-ctl evg spawn] SSH verify: skipped (no host_name in JSON)")
            return False
        user = host.get("user") or "ubuntu"
        ssh_key = Path.home() / ".ssh" / "evg-host"
        argv = [
            "ssh",
            "-i",
            str(ssh_key),
            "-o",
            "BatchMode=yes",
            "-o",
            "ConnectTimeout=5",
            "-o",
            "StrictHostKeyChecking=accept-new",
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


# ----------------------------------------------------------------------
# REST helpers (expiration). Module-level (no Runner) so unit tests can
# import without spinning up a domain instance.
# ----------------------------------------------------------------------


def _read_evg_config() -> Optional[dict]:
    """Parse ~/.evergreen.yml. Returns None when unreadable."""
    if not _EVG_CONFIG_PATH.is_file():
        return None
    try:
        import yaml  # type: ignore  # pyyaml — venv dep
    except ImportError:
        return None
    try:
        data = yaml.safe_load(_EVG_CONFIG_PATH.read_text())
    except (OSError, yaml.YAMLError):
        return None
    return data if isinstance(data, dict) else None


def _fetch_expiration_time(host_id: str) -> Optional[datetime]:
    """GET /rest/v2/hosts/{host_id} and return expiration_time as a UTC
    datetime, or None on any failure.
    """
    cfg = _read_evg_config()
    if not cfg:
        return None
    api_server = (cfg.get("api_server_host") or "").rstrip("/")
    api_user = cfg.get("user") or ""
    api_key = cfg.get("api_key") or ""
    if not (api_server and api_user and api_key):
        return None
    # api_server already ends in ``/api``; REST routes hang off ``/rest/v2``.
    url = f"{api_server}/rest/v2/hosts/{host_id}"
    req = urllib.request.Request(
        url,
        headers={
            "Api-User": api_user,
            "Api-Key": api_key,
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=_EVG_API_TIMEOUT_S) as resp:
            body = resp.read().decode("utf-8", errors="replace")
    except (urllib.error.URLError, urllib.error.HTTPError, OSError, ValueError):
        return None
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        return None
    raw = payload.get("expiration_time") if isinstance(payload, dict) else None
    if not raw:
        return None
    return _parse_rfc3339(raw)


def _parse_rfc3339(value: str) -> Optional[datetime]:
    """Parse the RFC3339 / ISO-8601 form Evergreen returns (e.g.
    ``2026-05-21T15:34:00Z``) into a tz-aware UTC datetime.
    """
    if not isinstance(value, str):
        return None
    # ``fromisoformat`` accepts trailing ``+00:00`` but not ``Z`` until 3.11.
    text = value.replace("Z", "+00:00")
    try:
        dt = datetime.fromisoformat(text)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def _format_remaining_secs(secs: int) -> str:
    """Human ``Xh Ym`` for ``secs`` remaining. Returns ``expired`` when
    non-positive.
    """
    if secs <= 0:
        return "expired"
    hours, rem = divmod(secs, 3600)
    minutes = rem // 60
    if hours == 0:
        return f"{minutes}m"
    return f"{hours}h{minutes:02d}m"
