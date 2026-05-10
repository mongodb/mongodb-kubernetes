"""Network prefix domain — **native Python registry** (Phase 2.5 + /23 expansion).

Replaces the bash ``dc_select_network.sh`` body. The bash file is now a thin
shim that execs ``wt-ctl network ...``. Public surface preserved for
backwards-compat with Phase 1 + Phase 2 callers:

  - ``NetworkDomain.list_entries()`` -> ``[NetEntry]``
  - ``NetworkDomain.list_raw()``     -> str (byte-compat with the bash
                                       ``--list`` output, modulo dynamic
                                       ``Worktree parent`` line)
  - ``NetworkDomain.prefix_for(branch_dir)``
  - ``NetworkDomain.prune(dry_run=False)`` -> str
  - ``NetworkDomain.release(branch_dir)``  -> str
  - ``NetworkDomain.allocate(branch_dir)`` -> str (always
                                       ``MCK_DEVC_NET_PREFIX=<N>\n`` plus,
                                       for new-scheme allocations, four
                                       companion lines for X/Y/PORT)

# Address-space layout — /23 per stack within 172.16.0.0/12

Index ``N`` (0..2047) maps to:

```
X       = 16 + (N >> 7)        # second octet, 16..31
Y_BASE  = (N & 0x7F) << 1      # third octet base, even, 0..254
Y_VIP   = Y_BASE + 1           # third octet for VIPs
PORT    = 8000 + N             # gost-proxy host port, 8000..10047
```

Per-stack:
- subnet:        ``172.X.Y_BASE.0/23`` (the docker network CIDR)
- ip_range:      ``172.X.Y_BASE.0/24``
- VIP_CIDR:      ``172.X.Y_VIP.0/24``
- proxy_address: ``172.X.Y_BASE.10``
- gost-proxy:    ``127.0.0.1:PORT``

# Legacy /16 scheme (16..31)

Pre-expansion stacks owned a full /16 (``172.NN.0.0/16`` for NN in 16..31).
We continue to recognise registry rows that carry only a bare integer in
that range, treating them as if X=NN, Y_BASE=0, Y_VIP=1, PORT=80NN.

When the new allocator picks a free index it skips any candidate whose
computed X falls inside a legacy entry's /16 — that "wastes" 128 indices
per legacy stack but keeps both schemes safe to coexist on the same host.

# Persisted format

```
<branch_dir>=<N>                        # legacy: 16 <= N <= 31
<branch_dir>=<N>:<X>:<Y_BASE>:<Y_VIP>:<PORT>   # new scheme
```

The ``Registry`` parser handles both. ``release`` removes by branch_dir
regardless of which form the line was written in. New allocations always
emit the composite form so all four derived octets/port round-trip
intact.

Subprocess discipline: this module **never** imports subprocess. Docker
network discovery and ``git worktree list`` go through the injected
``Runner`` (per ``runner.py`` contract).
"""

from __future__ import annotations

import os
import re
import sys
import time
from contextlib import contextmanager
from dataclasses import dataclass
from io import StringIO
from pathlib import Path
from typing import Iterator, Optional

from ..errors import (
    ExternalCommandFailed,
    LockTimeout,
    RegistryError,
    ToolMissing,
)
from ..runner import Runner
from ..state import NetEntry


# Index space — 0..MAX_INDEX (inclusive) maps to X/Y/PORT via stack_params().
INDEX_LO = 0
INDEX_HI = 2047

# Legacy /16 second-octet range. Backwards-compat parsers treat a bare
# integer in this band as a full /16 occupation.
LEGACY_X_LO = 16
LEGACY_X_HI = 31

# Public aliases preserved for any out-of-tree callers / tests.
VALID_RANGE_LO = INDEX_LO
VALID_RANGE_HI = INDEX_HI

LOCK_TIMEOUT_SECS = 30          # max wait to acquire a lock
LOCK_POLL_INTERVAL_SECS = 0.1
LOCK_STALE_SECS = 60            # force-release a lock dir older than this


# ---------------------------------------------------------------------------
# math
# ---------------------------------------------------------------------------

@dataclass(frozen=True)
class StackParams:
    """All four derived address-space parameters for a stack index ``N``.

    ``scheme='legacy'`` is reserved for /16-occupying entries: those still
    set ``index`` to the second octet (16..31) so callers that filter on
    ``index`` continue to work, but expose Y/PORT shaped like a degenerate
    /23 occupying the bottom of the /16.
    """
    index: int
    x: int
    y_base: int
    y_vip: int
    port: int
    scheme: str = "new"   # "new" | "legacy"

    @property
    def subnet(self) -> str:
        if self.scheme == "legacy":
            return f"172.{self.x}.0.0/16"
        return f"172.{self.x}.{self.y_base}.0/23"

    @property
    def ip_range(self) -> str:
        if self.scheme == "legacy":
            return f"172.{self.x}.0.0/24"
        return f"172.{self.x}.{self.y_base}.0/24"

    @property
    def vip_cidr(self) -> str:
        return f"172.{self.x}.{self.y_vip}.0/24"

    @property
    def proxy_address(self) -> str:
        return f"172.{self.x}.{self.y_base}.10"


def stack_params(index: int, *, scheme: str = "new") -> StackParams:
    """Compute (X, Y_BASE, Y_VIP, PORT) for stack index ``index``.

    For ``scheme='legacy'`` the index *is* the /16 second octet (16..31)
    and Y/PORT are pinned to the legacy-equivalent values
    (Y_BASE=0, Y_VIP=1, PORT=80<NN>).
    """
    if scheme == "legacy":
        if not (LEGACY_X_LO <= index <= LEGACY_X_HI):
            raise RegistryError(
                f"legacy prefix {index} outside [{LEGACY_X_LO},{LEGACY_X_HI}]"
            )
        return StackParams(
            index=index,
            x=index,
            y_base=0,
            y_vip=1,
            port=8000 + index,
            scheme="legacy",
        )
    if not (INDEX_LO <= index <= INDEX_HI):
        raise RegistryError(
            f"stack index {index} outside [{INDEX_LO},{INDEX_HI}]"
        )
    x = LEGACY_X_LO + (index >> 7)
    y_base = (index & 0x7F) << 1
    y_vip = y_base + 1
    port = 8000 + index
    return StackParams(
        index=index,
        x=x,
        y_base=y_base,
        y_vip=y_vip,
        port=port,
        scheme="new",
    )


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

def _registry_dir() -> Path:
    override = os.environ.get("MCK_DEVC_REGISTRY_DIR")
    if override:
        return Path(override)
    return Path.home() / ".cache" / "mck-devc"


def _registry_file() -> Path:
    return _registry_dir() / "net-prefix-registry"


def _lock_dir() -> Path:
    return _registry_dir() / "net-prefix-registry.lock.d"


def _ensure_registry() -> tuple[Path, Path]:
    rdir = _registry_dir()
    rdir.mkdir(parents=True, exist_ok=True)
    rfile = _registry_file()
    if not rfile.exists():
        rfile.touch()
    return rdir, rfile


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        # Process exists but is owned by someone else; treat as alive.
        return True
    except OSError:
        return False


# ---------------------------------------------------------------------------
# locking
# ---------------------------------------------------------------------------

@contextmanager
def _registry_lock(*, timeout: float = LOCK_TIMEOUT_SECS,
                   stale_after: float = LOCK_STALE_SECS,
                   warn: Optional[callable] = None) -> Iterator[None]:
    """mkdir-based lock with stale recovery. Drops a ``pid`` file inside
    the lock dir; if a stale lock blocks acquisition (dead PID or old
    mtime) we force-release with a stderr warning and proceed.
    """
    _ensure_registry()
    lock = _lock_dir()
    deadline = time.monotonic() + timeout
    forced = False
    while True:
        try:
            lock.mkdir(parents=False, exist_ok=False)
            break
        except FileExistsError:
            if not forced and _try_force_release(lock, stale_after, warn):
                forced = True
                continue
        if time.monotonic() >= deadline:
            raise LockTimeout(str(lock), timeout)
        time.sleep(LOCK_POLL_INTERVAL_SECS)
    pidfile = lock / "pid"
    try:
        pidfile.write_text(f"{os.getpid()}\n")
    except OSError:
        # Best-effort; don't fail the whole operation if pidfile write fails.
        pass
    try:
        yield
    finally:
        # Best-effort cleanup; tolerate the dir already being gone.
        try:
            if pidfile.exists():
                pidfile.unlink()
        except OSError:
            pass
        try:
            lock.rmdir()
        except (FileNotFoundError, OSError):
            pass


def _try_force_release(lock: Path, stale_after: float,
                       warn: Optional[callable]) -> bool:
    """Return True if we removed a stale lock dir, False otherwise."""
    if not lock.is_dir():
        return False
    pidfile = lock / "pid"
    pid_alive = False
    if pidfile.is_file():
        try:
            content = pidfile.read_text().strip()
            if content.isdigit():
                pid_alive = _pid_alive(int(content))
        except OSError:
            pid_alive = False
    age: float
    try:
        age = time.time() - lock.stat().st_mtime
    except OSError:
        age = 0.0
    is_stale = (not pid_alive) or age >= stale_after
    if not is_stale:
        return False
    msg = f"[wt-ctl] network: force-releasing stale lock at {lock} (age={int(age)}s, pid_alive={pid_alive})"
    if warn is not None:
        warn(msg)
    else:
        sys.stderr.write(msg + "\n")
    try:
        if pidfile.exists():
            pidfile.unlink()
    except OSError:
        pass
    try:
        lock.rmdir()
        return True
    except OSError:
        return False


# ---------------------------------------------------------------------------
# native Registry
# ---------------------------------------------------------------------------

@dataclass
class _OrphanNetwork:
    """Docker network in 172.[16-31].0.0/16 with name pattern
    ``*_devcontainer_devcontainer``, 0 attached containers, and no
    surviving worktree.
    """
    name: str
    prefix: int


@dataclass
class _DockerNet:
    name: str
    prefix: int


@dataclass(frozen=True)
class _RegistryRow:
    """One persisted registry line, parsed.

    For legacy entries ``params.scheme == 'legacy'`` and ``params.index``
    equals the second octet (16..31). For new entries
    ``params.scheme == 'new'`` and ``params.index`` is 0..2047.
    """
    branch_dir: str
    params: StackParams


class Registry:
    """Native Python implementation of the net-prefix registry.

    All public methods grab the lock as needed. Lock acquisition is
    re-entrant within a single ``allocate``/``prune`` call: each call
    grabs once, mutates atomically, releases.
    """

    def __init__(self, runner: Runner, *, worktree_parent: Path) -> None:
        self.runner = runner
        self.worktree_parent = worktree_parent.resolve()

    # ------------------------------------------------------------------
    # raw read/write
    # ------------------------------------------------------------------
    def _read(self) -> list[_RegistryRow]:
        _, rfile = _ensure_registry()
        out: list[_RegistryRow] = []
        try:
            text = rfile.read_text()
        except OSError as exc:
            raise RegistryError(f"failed to read {rfile}: {exc}") from exc
        for raw in text.splitlines():
            line = raw.strip()
            if not line or "=" not in line:
                continue
            bd, rhs = line.split("=", 1)
            bd = bd.strip()
            rhs = rhs.strip()
            if not bd or not rhs:
                continue
            row = _parse_rhs(bd, rhs)
            if row is None:
                continue
            out.append(row)
        return out

    def _write(self, rows: list[_RegistryRow]) -> None:
        _, rfile = _ensure_registry()
        body = "".join(_format_row(r) + "\n" for r in rows)
        try:
            rfile.write_text(body)
        except OSError as exc:
            raise RegistryError(f"failed to write {rfile}: {exc}") from exc

    # ------------------------------------------------------------------
    # branch_dir liveness check (mirrors bash is_stale_branch_dir)
    # ------------------------------------------------------------------
    def _is_stale_branch_dir(self, bd: str, *, git_basenames: set[str]) -> bool:
        if (self.worktree_parent / bd).is_dir():
            return False
        if bd in git_basenames:
            return False
        return True

    def _git_worktree_basenames(self, repo_root: Path) -> set[str]:
        """Return basenames from ``git worktree list --porcelain`` for the
        repo at ``repo_root``. Best-effort: returns empty on any failure
        (mirrors the bash script's tolerant `2>/dev/null`).
        """
        if not (repo_root / ".git").exists():
            return set()
        try:
            res = self.runner.run(
                ["git", "-C", str(repo_root), "worktree", "list", "--porcelain"],
                check=False,
            )
        except ToolMissing:
            return set()
        if res.rc != 0:
            return set()
        names: set[str] = set()
        for line in res.stdout.splitlines():
            if line.startswith("worktree "):
                p = line[len("worktree "):].strip()
                if p:
                    names.add(Path(p).name)
        return names

    # ------------------------------------------------------------------
    # docker network discovery (mirrors bash docker_networks_in_range)
    # ------------------------------------------------------------------
    def _docker_networks_in_range(self) -> list[_DockerNet]:
        try:
            res = self.runner.run(
                ["docker", "network", "ls", "--format", "{{.Name}}"],
                check=False,
            )
        except ToolMissing:
            return []
        if res.rc != 0:
            return []
        out: list[_DockerNet] = []
        subnet_re = re.compile(r"^172\.(\d+)\.")
        for raw in res.stdout.splitlines():
            name = raw.strip()
            if not name:
                continue
            try:
                ins = self.runner.run(
                    [
                        "docker", "network", "inspect", name,
                        "--format", "{{range .IPAM.Config}}{{.Subnet}}{{end}}",
                    ],
                    check=False,
                )
            except ToolMissing:
                return []
            if ins.rc != 0:
                continue
            subnet = ins.stdout.strip()
            m = subnet_re.match(subnet)
            if not m:
                continue
            x = int(m.group(1))
            if LEGACY_X_LO <= x <= LEGACY_X_HI:
                out.append(_DockerNet(name=name, prefix=x))
        return out

    def _docker_network_inspect_containers(self, name: str) -> Optional[int]:
        try:
            res = self.runner.run(
                [
                    "docker", "network", "inspect", name,
                    "--format", "{{len .Containers}}",
                ],
                check=False,
            )
        except ToolMissing:
            return None
        if res.rc != 0:
            return None
        out = res.stdout.strip()
        if out.isdigit():
            return int(out)
        return None

    # ------------------------------------------------------------------
    # public surface
    # ------------------------------------------------------------------
    def list(self, *, repo_root: Optional[Path] = None) -> tuple[list[NetEntry], list[NetEntry]]:
        """Return ``(active_or_stale_rows_in_registry_order, [])``.

        We return both lists for forward-compat with the plan's
        ``OrphanEntry`` notion, but in this codebase orphans are
        represented as rows with ``status="stale"`` inside the same list
        (matches the bash script's behavior). Callers that want the
        explicit orphan view can filter on status. The second tuple slot
        is reserved for future divergence.
        """
        with _registry_lock():
            return self._list_locked(repo_root=repo_root)

    def _list_locked(self, *, repo_root: Optional[Path]) -> tuple[list[NetEntry], list[NetEntry]]:
        rows = self._read()
        git_names: set[str] = set()
        if repo_root is not None:
            git_names = self._git_worktree_basenames(repo_root)
        entries: list[NetEntry] = []
        for row in rows:
            bd = row.branch_dir
            # ``NetEntry.prefix`` carries:
            #   - legacy: the second octet (16..31)
            #   - new:    the stack index (0..2047)
            # That makes the field load-bearing for the renderer (it shows
            # legacy entries' /16 numbers verbatim) without needing to
            # widen the dataclass mid-flight.
            pref_for_entry = (
                row.params.x if row.params.scheme == "legacy" else row.params.index
            )
            if self._is_stale_branch_dir(bd, git_basenames=git_names):
                entries.append(
                    NetEntry(
                        branch_dir=bd,
                        prefix=pref_for_entry,
                        status="stale",
                        worktree_path=None,
                    )
                )
            else:
                entries.append(
                    NetEntry(
                        branch_dir=bd,
                        prefix=pref_for_entry,
                        status="active",
                        worktree_path=str(self.worktree_parent / bd),
                    )
                )
        return entries, []

    def list_rows(self) -> list[_RegistryRow]:
        """Return the parsed registry rows verbatim (no liveness check).

        Exposes scheme/X/Y/PORT to callers (CLI ``network list`` renderer).
        """
        with _registry_lock():
            return self._read()

    def release(self, branch_dir: str, *, dry_run: bool = False) -> str:
        out = StringIO()
        with _registry_lock():
            rows = self._read()
            match: Optional[_RegistryRow] = next(
                (r for r in rows if r.branch_dir == branch_dir), None
            )
            if match is None:
                out.write(f"No registry entry for {branch_dir}; nothing to release.\n")
                return out.getvalue()
            label = _format_rhs(match.params)
            if dry_run:
                out.write(f"[dry-run] would release {branch_dir}={label}\n")
                return out.getvalue()
            kept = [r for r in rows if r.branch_dir != branch_dir]
            self._write(kept)
            out.write(f"Released {branch_dir}={label}.\n")
        return out.getvalue()

    def prune(self, *, dry_run: bool = False, prune_networks: bool = False,
              repo_root: Optional[Path] = None) -> str:
        """Drop stale registry entries. Optionally also remove orphan
        docker networks (``--networks`` in the bash CLI).

        Mirrors the bash script's stdout exactly enough that scripts /
        skills parsing the output continue to work.
        """
        out = StringIO()
        with _registry_lock():
            rows = self._read()
            git_names: set[str] = set()
            if repo_root is not None:
                git_names = self._git_worktree_basenames(repo_root)
            kept: list[_RegistryRow] = []
            removed = 0
            out.write("Scanning registry for stale entries…\n")
            for row in rows:
                bd = row.branch_dir
                label = _format_rhs(row.params)
                if self._is_stale_branch_dir(bd, git_basenames=git_names):
                    if dry_run:
                        out.write(f"[dry-run] stale: {bd}={label}\n")
                    else:
                        out.write(f"Pruning stale registry entry: {bd}={label}\n")
                    removed += 1
                else:
                    kept.append(row)
            if not dry_run and removed > 0:
                self._write(kept)
            verb = "would be" if dry_run else "were"
            out.write(f"Registry: {removed} stale entries {verb} removed.\n")

            if prune_networks:
                out.write("\n")
                out.write("Scanning docker networks for orphan devc compose stacks…\n")
                self._scan_orphan_networks(out, dry_run=dry_run, repo_root=repo_root, git_names=git_names)
        return out.getvalue()

    def _scan_orphan_networks(self, out: StringIO, *, dry_run: bool,
                              repo_root: Optional[Path],
                              git_names: set[str]) -> None:
        nets = self._docker_networks_in_range()
        for net in nets:
            if not net.name.endswith("_devcontainer_devcontainer"):
                continue
            containers = self._docker_network_inspect_containers(net.name)
            if containers != 0:
                continue
            project_name = net.name[: -len("_devcontainer")]
            branch_lc = project_name[: -len("_devcontainer")]
            # Compose normalizes project names to lowercase, so we
            # match worktrees case-insensitively.
            wt_lc = {n.lower() for n in git_names}
            wt_lc |= {p.name.lower() for p in self.worktree_parent.glob("*")} \
                     if self.worktree_parent.is_dir() else set()
            if branch_lc in wt_lc:
                out.write(
                    f"Skipping {net.name} (172.{net.prefix}.0.0/16): worktree '{branch_lc}' still exists. "
                    f"Use 'docker network rm {net.name}' if you really want it gone.\n"
                )
                continue
            if dry_run:
                out.write(f"[dry-run] orphan docker network: {net.name} (172.{net.prefix}.0.0/16)\n")
            else:
                out.write(f"Removing orphan docker network: {net.name} (172.{net.prefix}.0.0/16)\n")
                try:
                    res = self.runner.run(
                        ["docker", "network", "rm", net.name],
                        check=False,
                    )
                    body = (res.stdout or "") + (res.stderr or "")
                    for line in body.splitlines():
                        out.write(f"    {line}\n")
                except ToolMissing:
                    out.write("    (docker missing — cannot remove)\n")

    def allocate(self, branch_dir: Optional[str] = None, *,
                 auto_prune: bool = True,
                 repo_root: Optional[Path] = None,
                 emit_warning: Optional[callable] = None) -> StackParams:
        """Allocate (or return existing) stack parameters for ``branch_dir``.

        Returns a ``StackParams``. Callers that just want the index can
        read ``.index``. The CLI wrapper composes the env-var lines.

        ``MCK_DEVC_NET_PREFIX`` env override is honored — caller takes
        ownership; we skip both the registry and the docker scan and
        return a synthesized StackParams (legacy when 16..31, new
        otherwise).
        """
        env_pref = os.environ.get("MCK_DEVC_NET_PREFIX")
        if env_pref:
            if env_pref.isdigit():
                v = int(env_pref)
                if LEGACY_X_LO <= v <= LEGACY_X_HI:
                    # Honor the legacy contract — env override at a /16
                    # band always means "use that whole /16".
                    return stack_params(v, scheme="legacy")
                if INDEX_LO <= v <= INDEX_HI:
                    return stack_params(v, scheme="new")
            raise RegistryError(
                f"MCK_DEVC_NET_PREFIX='{env_pref}' is not a valid stack index "
                f"(legacy /16: [{LEGACY_X_LO},{LEGACY_X_HI}]; "
                f"new index: [{INDEX_LO},{INDEX_HI}])"
            )

        warn = emit_warning if emit_warning is not None else (lambda m: sys.stderr.write(m + "\n"))

        with _registry_lock(warn=warn):
            rows = self._read()

            # Idempotent: branch already has a registration?
            if branch_dir:
                for row in rows:
                    if row.branch_dir == branch_dir:
                        return row.params

            # Auto-prune stale entries.
            if auto_prune and rows:
                git_names = self._git_worktree_basenames(repo_root) if repo_root else set()
                kept: list[_RegistryRow] = []
                pruned = 0
                for row in rows:
                    if self._is_stale_branch_dir(row.branch_dir, git_basenames=git_names):
                        warn(f"Auto-pruning stale registry entry: {row.branch_dir}={_format_rhs(row.params)}")
                        pruned += 1
                    else:
                        kept.append(row)
                if pruned > 0:
                    self._write(kept)
                    rows = kept

            # Build "blocked second octets" from legacy registry entries +
            # any /16 docker network in the legacy band. Each blocked X
            # eats 128 candidate indices (those with X(N) == that octet).
            blocked_xs: set[int] = set()
            used_indices: set[int] = set()
            for row in rows:
                if row.params.scheme == "legacy":
                    # Legacy entry occupies the entire 172.NN.0.0/16 — block
                    # every new index whose X falls in there. Also reserve
                    # the raw value (16..31) as a used index: ``root-context``
                    # builds NAMESPACE=<base>-<MCK_DEVC_NET_PREFIX>, so two
                    # stacks with the same numeric value would collide on
                    # namespace even though their IP ranges don't.
                    blocked_xs.add(row.params.x)
                    used_indices.add(row.params.index)
                else:
                    used_indices.add(row.params.index)
            for net in self._docker_networks_in_range():
                blocked_xs.add(net.prefix)

            for cand in range(INDEX_LO, INDEX_HI + 1):
                if cand in used_indices:
                    continue
                params = stack_params(cand, scheme="new")
                if params.x in blocked_xs:
                    continue
                if branch_dir:
                    rows.append(_RegistryRow(branch_dir=branch_dir, params=params))
                    self._write(rows)
                return params

            raise RegistryError(
                "no free stack index available "
                f"(used_indices={sorted(used_indices)}, blocked_xs={sorted(blocked_xs)})"
            )

    # ------------------------------------------------------------------
    # rendering — must byte-match the bash --list output (modulo dynamic
    # `Worktree parent` line, which depends on caller cwd)
    # ------------------------------------------------------------------
    def render_list(self, *, repo_root: Optional[Path] = None,
                    script_self: Optional[str] = None) -> str:
        """Return the full ``--list`` text body (registry table +
        summary + docker networks + pruning hints). Byte-compatible with
        the legacy bash output for the columns it always carried; the new
        scheme/X/Y/PORT data is shown in a small extension table after
        the canonical PREFIX/STATUS/WORKTREE table so existing parsers
        keep working.
        """
        out = StringIO()
        rfile = _registry_file()
        out.write(f"Registry: {rfile}\n")
        out.write(f"Worktree parent (conventional): {self.worktree_parent}\n")
        out.write("\n")

        # Header (column widths chosen to match the bash printf format).
        out.write(f"{'BRANCH_DIR':<55}  {'PREFIX':<6}  {'STATUS':<6}  WORKTREE\n")
        out.write(f"{'----------':<55}  {'------':<6}  {'------':<6}  --------\n")

        with _registry_lock():
            entries, _ = self._list_locked(repo_root=repo_root)
            rows = self._read()
            row_by_bd = {r.branch_dir: r for r in rows}
            stale = active = 0
            for e in entries:
                if e.status == "stale":
                    out.write(f"{e.branch_dir:<55}  {e.prefix:<6}  {'stale':<6}  (missing)\n")
                    stale += 1
                else:
                    wt = e.worktree_path or ""
                    out.write(f"{e.branch_dir:<55}  {e.prefix:<6}  {'active':<6}  {wt}\n")
                    active += 1
            out.write("\n")
            out.write(
                f"Summary: {active} active, {stale} stale (run with --prune to GC).\n"
            )

            # Stack-params extension table: shows N/X/Y_BASE/Y_VIP/PORT
            # for both schemes. Only emitted when the registry is non-
            # empty so the legacy fixture (empty registry) round-trips
            # byte-identical.
            if rows:
                out.write("\n")
                out.write(f"{'BRANCH_DIR':<55}  {'N':>5}  {'X':>3}  {'Y_BASE':>6}  {'Y_VIP':>5}  {'PORT':>5}  SCHEME\n")
                out.write(f"{'----------':<55}  {'-':>5}  {'-':>3}  {'------':>6}  {'-----':>5}  {'----':>5}  ------\n")
                for row in rows:
                    p = row.params
                    out.write(
                        f"{row.branch_dir:<55}  "
                        f"{p.index:>5}  {p.x:>3}  {p.y_base:>6}  {p.y_vip:>5}  {p.port:>5}  "
                        f"{p.scheme}\n"
                    )

            out.write("\n")
            out.write(
                f"Docker networks in 172.[{LEGACY_X_LO}-{LEGACY_X_HI}].0.0/16:\n"
            )
            out.write(f"{'NETWORK':<65}  PREFIX\n")
            out.write(f"{'-------':<65}  ------\n")
            nets = sorted(self._docker_networks_in_range(), key=lambda n: n.prefix)
            for net in nets:
                out.write(f"{net.name:<65}  {net.prefix}\n")
            out.write("\n")
            out.write(
                f"Free range: 172.[{LEGACY_X_LO}-{LEGACY_X_HI}].0.0/16 (legacy) "
                f"+ 172.16.0.0/12 /23 stacks (new scheme, indices [{INDEX_LO},{INDEX_HI}]).\n"
            )
            out.write("Used by registry + docker networks listed above are blocked.\n")
            out.write("\n")
            # Render the pruning hints with the legacy bash flag style when
            # ``script_self`` points at a `dc_select_network.sh` path — that
            # keeps the output byte-equal to the pre-Phase-2.5 bash version
            # so skills/scripts grepping the line continue to work. When
            # invoked through the new entry-point name (``wt-ctl network``)
            # we use the new subcommand spelling.
            self_ref = script_self or "wt-ctl network"
            legacy_style = self_ref.endswith("dc_select_network.sh")
            if legacy_style:
                stale_arg = "--prune"
                nets_arg = "--prune --networks"
                rel_arg = "--release"
            else:
                stale_arg = "prune"
                nets_arg = "prune --networks"
                rel_arg = "release"
            out.write("Pruning info:\n")
            out.write(f"  - Stale registry entries:  {self_ref} {stale_arg} [--dry-run]\n")
            out.write(f"  - Plus orphan docker nets: {self_ref} {nets_arg} [--dry-run]\n")
            out.write(f"  - Specific entry:          {self_ref} {rel_arg} <branch_dir>\n")
            tear_ref = "wt-ctl delete" if not legacy_style else "wt_teardown.sh"
            out.write(f"  - When you tear down a worktree via {tear_ref}, the registry\n")
            out.write("    entry is released automatically.\n")
        return out.getvalue()


# ---------------------------------------------------------------------------
# row parsing/formatting — handles BOTH legacy (bare int) and new
# (composite ``N:X:Y:VIP:PORT``) registry rows.
# ---------------------------------------------------------------------------

def _parse_rhs(branch_dir: str, rhs: str) -> Optional[_RegistryRow]:
    """Parse the right-hand side of a registry line.

    Accepts:
      - bare ``<N>`` where 16 <= N <= 31  -> legacy /16 occupation
      - ``<N>:<X>:<Y_BASE>:<Y_VIP>:<PORT>`` -> new scheme

    Returns ``None`` for any malformed line so the registry tolerates a
    user hand-edit (mirrors the bash parser's tolerance).
    """
    if ":" in rhs:
        parts = rhs.split(":")
        if len(parts) != 5:
            return None
        try:
            n, x, y_base, y_vip, port = (int(s) for s in parts)
        except ValueError:
            return None
        if not (INDEX_LO <= n <= INDEX_HI):
            return None
        # Trust the persisted X/Y/PORT verbatim (don't recompute) so a
        # human-edited registry round-trips. We DO sanity-check that
        # it's internally consistent — if a row claims a different X
        # than stack_params says, log and skip rather than mis-route
        # traffic.
        derived = stack_params(n, scheme="new")
        if (x, y_base, y_vip, port) != (derived.x, derived.y_base, derived.y_vip, derived.port):
            sys.stderr.write(
                f"[wt-ctl] network: WARN registry row {branch_dir}={rhs} "
                f"is internally inconsistent — using derived values.\n"
            )
            params = derived
        else:
            params = StackParams(
                index=n, x=x, y_base=y_base, y_vip=y_vip, port=port, scheme="new"
            )
        return _RegistryRow(branch_dir=branch_dir, params=params)
    # Bare integer: legacy /16 or — for forward-compat — a new-scheme
    # index without companion fields. We ONLY accept the legacy band
    # here; a bare number outside [16,31] is treated as malformed.
    try:
        v = int(rhs)
    except ValueError:
        return None
    if LEGACY_X_LO <= v <= LEGACY_X_HI:
        return _RegistryRow(
            branch_dir=branch_dir,
            params=stack_params(v, scheme="legacy"),
        )
    return None


def _format_rhs(p: StackParams) -> str:
    if p.scheme == "legacy":
        return str(p.x)  # round-trip the bare-int form
    return f"{p.index}:{p.x}:{p.y_base}:{p.y_vip}:{p.port}"


def _format_row(row: _RegistryRow) -> str:
    return f"{row.branch_dir}={_format_rhs(row.params)}"


# ---------------------------------------------------------------------------
# .env migration helper
# ---------------------------------------------------------------------------

# Names of the four derived env-vars compose.yml reads.
DERIVED_ENV_KEYS = (
    "MCK_DEVC_NET_X",
    "MCK_DEVC_NET_Y_BASE",
    "MCK_DEVC_NET_Y_VIP",
    "MCK_DEVC_PROXY_PORT",
)


def env_lines_for(params: StackParams) -> list[str]:
    """Return the 5 ``KEY=VALUE`` lines compose.yml expects, in order:
    ``MCK_DEVC_NET_PREFIX``, then the four derived vars.
    """
    return [
        f"MCK_DEVC_NET_PREFIX={params.index}",
        f"MCK_DEVC_NET_X={params.x}",
        f"MCK_DEVC_NET_Y_BASE={params.y_base}",
        f"MCK_DEVC_NET_Y_VIP={params.y_vip}",
        f"MCK_DEVC_PROXY_PORT={params.port}",
    ]


def migrate_legacy_env(env_file: Path) -> tuple[bool, str]:
    """Append the four derived MCK_DEVC_NET_* vars to ``env_file`` if it
    has only the legacy bare ``MCK_DEVC_NET_PREFIX`` line.

    Returns ``(changed, message)``:
      - ``changed=True`` when the file was modified
      - ``changed=False`` when the file already had all four derived vars
        (idempotent re-run) or the file is missing / has no prefix line
        (we don't manufacture state)

    Behavior:
      - If MCK_DEVC_NET_PREFIX is in the legacy band (16..31), derive
        params via ``stack_params(scheme='legacy')`` and append any
        missing derived var lines.
      - If MCK_DEVC_NET_PREFIX is in the new index band (0..2047) and at
        least one of the derived vars is missing, append the missing
        ones from ``stack_params(scheme='new')``.
      - If all four derived vars are already present, no-op.
    """
    if not env_file.is_file():
        return False, f"{env_file}: not present (skipping)"
    try:
        text = env_file.read_text()
    except OSError as exc:
        raise RegistryError(f"failed to read {env_file}: {exc}") from exc

    parsed: dict[str, str] = {}
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        parsed[k.strip()] = v.strip()

    pref_str = parsed.get("MCK_DEVC_NET_PREFIX")
    if pref_str is None or not pref_str.isdigit():
        return False, f"{env_file}: no MCK_DEVC_NET_PREFIX (skipping)"
    pref = int(pref_str)

    if LEGACY_X_LO <= pref <= LEGACY_X_HI:
        params = stack_params(pref, scheme="legacy")
    elif INDEX_LO <= pref <= INDEX_HI:
        params = stack_params(pref, scheme="new")
    else:
        return False, f"{env_file}: prefix {pref} out of range (skipping)"

    missing: list[str] = []
    desired = {
        "MCK_DEVC_NET_X": str(params.x),
        "MCK_DEVC_NET_Y_BASE": str(params.y_base),
        "MCK_DEVC_NET_Y_VIP": str(params.y_vip),
        "MCK_DEVC_PROXY_PORT": str(params.port),
    }
    for key, val in desired.items():
        if key not in parsed:
            missing.append(f"{key}={val}")

    if not missing:
        return False, f"{env_file}: already has all derived vars (no change)"

    # Append the missing lines; preserve the trailing newline state.
    sep = "" if text.endswith("\n") or not text else "\n"
    addition = sep + "\n".join(missing) + "\n"
    try:
        with env_file.open("a") as fh:
            fh.write(addition)
    except OSError as exc:
        raise RegistryError(f"failed to write {env_file}: {exc}") from exc
    return True, (
        f"{env_file}: appended {len(missing)} derived var(s) "
        f"(scheme={params.scheme}, N={params.index})"
    )


# ---------------------------------------------------------------------------
# NetworkDomain — adapter that preserves the Phase-1 public surface
# ---------------------------------------------------------------------------

_LIST_HEADER = re.compile(r"^BRANCH_DIR\s+PREFIX\s+STATUS\s+WORKTREE\s*$")
_DOCKER_HEADER = re.compile(r"^Docker networks in 172\.\[")


class NetworkDomain:
    """Phase-1 callers got a ``NetworkDomain(runner, repo_root)``. We keep
    that signature. ``repo_root`` is the worktree root from which we
    derive (a) the conventional worktree-parent dir for stale checks and
    (b) the git repo to query ``git worktree list``.
    """

    def __init__(self, runner: Runner, repo_root: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root
        # The bash script lives at ``<wt>/scripts/dev/dc_select_network.sh``
        # for callers that still want to refer to it.
        self.script = repo_root / "scripts/dev/dc_select_network.sh"
        self._registry = Registry(runner, worktree_parent=repo_root.parent)

    # ------------------------------------------------------------------
    @property
    def registry(self) -> Registry:
        return self._registry

    # ------------------------------------------------------------------
    def list_entries(self) -> list[NetEntry]:
        entries, _ = self._registry.list(repo_root=self.repo_root)
        return entries

    def list_raw(self, *, script_self: Optional[str] = None) -> str:
        return self._registry.render_list(
            repo_root=self.repo_root, script_self=script_self
        )

    # ------------------------------------------------------------------
    def prefix_for(self, branch_dir: str) -> Optional[int]:
        for entry in self.list_entries():
            if entry.branch_dir == branch_dir:
                return entry.prefix
        return None

    # ------------------------------------------------------------------
    def prune(self, *, dry_run: bool = False, prune_networks: bool = False) -> str:
        return self._registry.prune(
            dry_run=dry_run,
            prune_networks=prune_networks,
            repo_root=self.repo_root,
        )

    def release(self, branch_dir: str, *, dry_run: bool = False) -> str:
        return self._registry.release(branch_dir, dry_run=dry_run)

    def allocate(self, branch_dir: Optional[str] = None) -> str:
        """Allocate a stack and return the env-var block compose.yml needs.

        Output format (terminated, multiple lines):

            MCK_DEVC_NET_PREFIX=<N>
            MCK_DEVC_NET_X=<X>
            MCK_DEVC_NET_Y_BASE=<Y_BASE>
            MCK_DEVC_NET_Y_VIP=<Y_VIP>
            MCK_DEVC_PROXY_PORT=<PORT>

        Legacy-band allocations (env override at 16..31) emit the same
        block — the four derived vars are pinned to legacy-equivalent
        values (Y_BASE=0, Y_VIP=1, PORT=80NN).
        """
        params = self._registry.allocate(
            branch_dir=branch_dir,
            auto_prune=True,
            repo_root=self.repo_root,
        )
        return "\n".join(env_lines_for(params)) + "\n"


# ---------------------------------------------------------------------------
# legacy text parser — kept for tests + any caller that still has bash
# output stored in a fixture
# ---------------------------------------------------------------------------

def _parse_list_output(text: str) -> list[NetEntry]:
    out: list[NetEntry] = []
    in_table = False
    for line in text.splitlines():
        if _LIST_HEADER.match(line):
            in_table = True
            continue
        if not in_table:
            continue
        if not line.strip():
            in_table = False
            continue
        if line.startswith("Summary:"):
            in_table = False
            continue
        if line.startswith("---"):
            continue
        if _DOCKER_HEADER.match(line):
            in_table = False
            continue
        parts = line.split()
        if len(parts) < 3:
            continue
        branch_dir = parts[0]
        try:
            prefix = int(parts[1])
        except ValueError:
            continue
        status = parts[2]
        worktree_path = " ".join(parts[3:]) if len(parts) >= 4 else None
        if worktree_path == "(missing)":
            worktree_path = None
        out.append(
            NetEntry(
                branch_dir=branch_dir,
                prefix=prefix,
                status=status,
                worktree_path=worktree_path,
            )
        )
    return out


# ---------------------------------------------------------------------------
# small pure helpers (used by status renderer)
# ---------------------------------------------------------------------------

def subnet_for(prefix: int) -> str:
    """Status-renderer helper. Preserves the legacy `/16` rendering for
    legacy-band values (16..31) so existing worktrees' status output
    doesn't change shape mid-migration. New-scheme indices render as
    ``172.X.Y_BASE.0/23``.
    """
    if LEGACY_X_LO <= prefix <= LEGACY_X_HI:
        # Legacy band: keep the /16 rendering for back-compat.
        return f"172.{prefix}.0.0/16"
    if INDEX_LO <= prefix <= INDEX_HI:
        p = stack_params(prefix, scheme="new")
        return p.subnet
    return f"172.{prefix}.0.0/16"  # out-of-range fallback (renders something)


def gost_proxy_for(prefix: int) -> str:
    """Map a stack index (or legacy /16 second octet) to the gost-proxy URL.

    Legacy band -> ``http://127.0.0.1:80<NN>`` (port 8016..8031). Same
    formula as the new scheme (port = 8000 + index), which is why the
    legacy renderer keeps working unchanged.
    """
    if LEGACY_X_LO <= prefix <= LEGACY_X_HI:
        return f"http://127.0.0.1:80{prefix:02d}"
    if INDEX_LO <= prefix <= INDEX_HI:
        port = 8000 + prefix
        return f"http://127.0.0.1:{port}"
    return f"http://127.0.0.1:80{prefix:02d}"


def namespace_for_prefix(prefix: int, base: str = "mck-devc") -> str:
    """NAMESPACE construction continues to use the prefix verbatim — for
    legacy entries that's the /16 second octet, for new it's the stack
    index. Either way the namespace is unique per stack.
    """
    return f"{base}-{prefix}-mongodb-test"


def parse_devc_env(devcontainer_dir: Path) -> dict[str, str]:
    """Tiny KEY=VALUE parser for ``.devcontainer/.env``."""
    out: dict[str, str] = {}
    env_file = devcontainer_dir / ".env"
    if not env_file.is_file():
        return out
    try:
        for raw in env_file.read_text().splitlines():
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            if "=" not in line:
                continue
            k, v = line.split("=", 1)
            out[k.strip()] = v.strip()
    except OSError as exc:
        raise RegistryError(f"failed to read {env_file}: {exc}") from exc
    return out
