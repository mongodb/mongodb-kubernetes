"""Network prefix domain — native Python registry.

Public surface:

  - ``NetworkDomain.list_entries()`` -> ``[NetEntry]``
  - ``NetworkDomain.list_raw()``     -> str (rendered table for ``network list``)
  - ``NetworkDomain.prefix_for(branch_dir)``
  - ``NetworkDomain.prune(dry_run=False)`` -> str
  - ``NetworkDomain.release(branch_dir)``  -> str
  - ``NetworkDomain.allocate(branch_dir)`` -> str (5 ``KEY=VALUE`` lines:
        ``MCK_DEVC_NET_PREFIX``, ``MCK_DEVC_NET_X``, ``MCK_DEVC_NET_Y_BASE``,
        ``MCK_DEVC_NET_Y_VIP``, ``MCK_DEVC_PROXY_PORT``)

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

# Persisted format

```
<branch_dir>=<N>:<X>:<Y_BASE>:<Y_VIP>:<PORT>
```

Subprocess discipline: this module **never** imports subprocess. Docker
network discovery and ``git worktree list`` go through the injected
``Runner`` (per ``runner.py`` contract).
"""

from __future__ import annotations

import fcntl
import ipaddress
import os
import re
import sys
import tempfile
import time
from contextlib import contextmanager
from dataclasses import dataclass
from io import StringIO
from pathlib import Path
from typing import Iterator, Optional

from ..errors import ExternalCommandFailed, LockTimeout, RegistryError, ToolMissing
from ..runner import Runner
from ..state import NetEntry

# Index space — 0..MAX_INDEX (inclusive) maps to X/Y/PORT via stack_params().
INDEX_LO = 0
INDEX_HI = 2047

# Public aliases for out-of-tree callers / tests.
VALID_RANGE_LO = INDEX_LO
VALID_RANGE_HI = INDEX_HI

LOCK_TIMEOUT_SECS = 30  # max wait to acquire a lock
LOCK_POLL_INTERVAL_SECS = 0.1


# ---------------------------------------------------------------------------
# math
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class StackParams:
    """All four derived address-space parameters for a stack index ``N``."""

    index: int
    x: int
    y_base: int
    y_vip: int
    port: int

    @property
    def subnet(self) -> str:
        return f"172.{self.x}.{self.y_base}.0/23"

    @property
    def ip_range(self) -> str:
        return f"172.{self.x}.{self.y_base}.0/24"

    @property
    def vip_cidr(self) -> str:
        return f"172.{self.x}.{self.y_vip}.0/24"

    @property
    def proxy_address(self) -> str:
        return f"172.{self.x}.{self.y_base}.10"


def stack_params(index: int) -> StackParams:
    """Compute (X, Y_BASE, Y_VIP, PORT) for stack index ``index``."""
    if not (INDEX_LO <= index <= INDEX_HI):
        raise RegistryError(f"stack index {index} outside [{INDEX_LO},{INDEX_HI}]")
    x = 16 + (index >> 7)
    y_base = (index & 0x7F) << 1
    y_vip = y_base + 1
    port = 8000 + index
    return StackParams(
        index=index,
        x=x,
        y_base=y_base,
        y_vip=y_vip,
        port=port,
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


def _lock_file() -> Path:
    return _registry_dir() / "net-prefix-registry.lock"


def _ensure_registry() -> tuple[Path, Path]:
    rdir = _registry_dir()
    rdir.mkdir(parents=True, exist_ok=True)
    rfile = _registry_file()
    if not rfile.exists():
        rfile.touch()
    return rdir, rfile


# ---------------------------------------------------------------------------
# locking
# ---------------------------------------------------------------------------


@contextmanager
def _registry_lock(*, timeout: float = LOCK_TIMEOUT_SECS) -> Iterator[None]:
    """Exclusive advisory lock via ``fcntl.flock`` on a dedicated lock file.

    The kernel releases the lock automatically when the holding process exits
    (or the fd closes), so there is no stale-lock heuristic to get wrong and no
    way to steal a lock a live process still holds. We poll ``LOCK_NB`` until
    ``timeout``.

    flock is on the open file *description*: two contenders (threads or
    processes) each ``os.open`` the lock file, so they get distinct
    descriptions and mutually exclude even within one process. Targets are the
    Linux devcontainer and macOS host, both of which support flock.
    """
    _ensure_registry()
    lockfile = _lock_file()
    deadline = time.monotonic() + timeout
    fd = os.open(str(lockfile), os.O_RDWR | os.O_CREAT | os.O_CLOEXEC, 0o644)
    try:
        while True:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except OSError:
                if time.monotonic() >= deadline:
                    raise LockTimeout(str(lockfile), timeout)
                time.sleep(LOCK_POLL_INTERVAL_SECS)
        try:  # holder pid, purely informational for humans debugging a stuck lock
            os.ftruncate(fd, 0)
            os.write(fd, f"{os.getpid()}\n".encode())
        except OSError:
            pass
        try:
            yield
        finally:
            try:
                fcntl.flock(fd, fcntl.LOCK_UN)
            except OSError:
                pass
    finally:
        os.close(fd)


# ---------------------------------------------------------------------------
# native Registry
# ---------------------------------------------------------------------------


@dataclass
class _OrphanNetwork:
    """Docker network in 172.[16-31].x.x with name pattern
    ``*_devcontainer_devcontainer``, 0 attached containers, and no
    surviving worktree.
    """

    name: str
    prefix: int


@dataclass
class _DockerNet:
    name: str
    prefix: int  # X octet (172.<prefix>.x.x)
    subnet: str = ""  # full CIDR, e.g. "172.18.0.0/16" or "172.16.4.0/23"


@dataclass(frozen=True)
class _RegistryRow:
    """One persisted registry line, parsed.

    ``params.index`` is 0..2047 (a /23 stack index).
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
                sys.stderr.write(f"[wt-ctl] network: WARN dropping unparseable registry " f"row {bd}={rhs}\n")
                continue
            out.append(row)
        return out

    def _write(self, rows: list[_RegistryRow]) -> None:
        rdir, rfile = _ensure_registry()
        body = "".join(_format_row(r) + "\n" for r in rows)
        # Atomic replace: write a tempfile sibling then os.replace, so a crash
        # mid-write can never truncate the registry and lose live allocations
        # (which would let a later allocate() collide with a running stack).
        try:
            fd, tmp = tempfile.mkstemp(prefix=".net-prefix-registry.", suffix=".tmp", dir=str(rdir))
            try:
                with os.fdopen(fd, "w") as fh:
                    fh.write(body)
                    fh.flush()
                    os.fsync(fh.fileno())
                os.replace(tmp, str(rfile))
            except BaseException:
                try:
                    os.unlink(tmp)
                except OSError:
                    pass
                raise
        except OSError as exc:
            raise RegistryError(f"failed to write {rfile}: {exc}") from exc

    # ------------------------------------------------------------------
    # branch_dir liveness check
    # ------------------------------------------------------------------
    def _is_stale_branch_dir(self, bd: str, *, git_basenames: set[str]) -> bool:
        if (self.worktree_parent / bd).is_dir():
            return False
        if bd in git_basenames:
            return False
        return True

    def _git_worktree_basenames(self, repo_root: Path) -> set[str]:
        """Return basenames from ``git worktree list --porcelain`` for the
        repo at ``repo_root``. Best-effort: returns empty on any failure.
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
                p = line[len("worktree ") :].strip()
                if p:
                    names.add(Path(p).name)
        return names

    # ------------------------------------------------------------------
    # docker network discovery
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
                        "docker",
                        "network",
                        "inspect",
                        name,
                        "--format",
                        "{{range .IPAM.Config}}{{.Subnet}}{{end}}",
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
            if 16 <= x <= 31:
                out.append(_DockerNet(name=name, prefix=x, subnet=subnet))
        return out

    def _docker_network_inspect_containers(self, name: str) -> Optional[int]:
        try:
            res = self.runner.run(
                [
                    "docker",
                    "network",
                    "inspect",
                    name,
                    "--format",
                    "{{len .Containers}}",
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

        Orphans are represented as rows with ``status="stale"`` inside the
        first list; callers wanting the explicit orphan view filter on status.
        The empty second slot is reserved for future divergence.
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
            if self._is_stale_branch_dir(bd, git_basenames=git_names):
                entries.append(
                    NetEntry(
                        branch_dir=bd,
                        prefix=row.params.index,
                        status="stale",
                        worktree_path=None,
                    )
                )
            else:
                entries.append(
                    NetEntry(
                        branch_dir=bd,
                        prefix=row.params.index,
                        status="active",
                        worktree_path=str(self.worktree_parent / bd),
                    )
                )
        return entries, []

    def list_rows(self) -> list[_RegistryRow]:
        """Return the parsed registry rows verbatim (no liveness check).

        Exposes index/X/Y/PORT to callers (CLI ``network list`` renderer).
        """
        with _registry_lock():
            return self._read()

    def release(self, branch_dir: str, *, dry_run: bool = False) -> str:
        out = StringIO()
        with _registry_lock():
            rows = self._read()
            match: Optional[_RegistryRow] = next((r for r in rows if r.branch_dir == branch_dir), None)
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

    def prune(self, *, dry_run: bool = False, prune_networks: bool = False, repo_root: Optional[Path] = None) -> str:
        """Drop stale registry entries. Optionally also remove orphan
        docker networks (``--networks``).
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

    def _scan_orphan_networks(
        self, out: StringIO, *, dry_run: bool, repo_root: Optional[Path], git_names: set[str]
    ) -> None:
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
            wt_lc |= (
                {p.name.lower() for p in self.worktree_parent.glob("*")} if self.worktree_parent.is_dir() else set()
            )
            if branch_lc in wt_lc:
                out.write(
                    f"Skipping {net.name} (172.{net.prefix}.x.x): worktree '{branch_lc}' still exists. "
                    f"Use 'docker network rm {net.name}' if you really want it gone.\n"
                )
                continue
            if dry_run:
                out.write(f"[dry-run] orphan docker network: {net.name} (172.{net.prefix}.x.x)\n")
            else:
                out.write(f"Removing orphan docker network: {net.name} (172.{net.prefix}.x.x)\n")
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

    def allocate(
        self,
        branch_dir: Optional[str] = None,
        *,
        auto_prune: bool = True,
        repo_root: Optional[Path] = None,
        emit_warning: Optional[callable] = None,
    ) -> StackParams:
        """Allocate (or return existing) stack parameters for ``branch_dir``.

        Returns a ``StackParams``. Callers that just want the index can
        read ``.index``. The CLI wrapper composes the env-var lines.

        ``MCK_DEVC_NET_PREFIX`` env override is honored — caller takes
        ownership; we skip both the registry and the docker scan and
        return synthesized StackParams.
        """
        env_pref = os.environ.get("MCK_DEVC_NET_PREFIX")
        if env_pref:
            if env_pref.isdigit():
                v = int(env_pref)
                if INDEX_LO <= v <= INDEX_HI:
                    return stack_params(v)
            raise RegistryError(
                f"MCK_DEVC_NET_PREFIX='{env_pref}' is not a valid stack index " f"[{INDEX_LO},{INDEX_HI}]"
            )

        warn = emit_warning if emit_warning is not None else (lambda m: sys.stderr.write(m + "\n"))

        with _registry_lock():
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

            used_indices: set[int] = {row.params.index for row in rows}

            # A docker network blocks only the /23 slots its CIDR overlaps: a
            # foreign /16 (docker bridge, kind, k3d) fills its whole X octet,
            # but a sibling /23 compose network blocks only its own slot and
            # leaves the other 127 in that octet free to allocate.
            docker_nets = self._docker_networks_in_range()
            blocked_subnets = [ipaddress.ip_network(net.subnet, strict=False) for net in docker_nets]

            for cand in range(INDEX_LO, INDEX_HI + 1):
                if cand in used_indices:
                    continue
                params = stack_params(cand)
                slot = ipaddress.ip_network(params.subnet)
                if any(slot.overlaps(blocked) for blocked in blocked_subnets):
                    continue
                if branch_dir:
                    rows.append(_RegistryRow(branch_dir=branch_dir, params=params))
                    self._write(rows)
                return params

            raise RegistryError(
                "no free stack index available "
                f"({len(used_indices)} registry slots used, "
                f"{len(docker_nets)} docker networks in 172.[16-31].x.x block the rest; "
                "run 'wt-ctl network prune --networks' to reclaim orphans)"
            )

    # ------------------------------------------------------------------
    # rendering
    # ------------------------------------------------------------------
    def render_list(self, *, repo_root: Optional[Path] = None, script_self: Optional[str] = None) -> str:
        """Return the full ``network list`` text body (registry table +
        summary + docker networks + pruning hints).
        """
        out = StringIO()
        rfile = _registry_file()
        out.write(f"Registry: {rfile}\n")
        out.write(f"Worktree parent (conventional): {self.worktree_parent}\n")
        out.write("\n")

        out.write(f"{'BRANCH_DIR':<55}  {'PREFIX':<6}  {'STATUS':<6}  WORKTREE\n")
        out.write(f"{'----------':<55}  {'------':<6}  {'------':<6}  --------\n")

        with _registry_lock():
            entries, _ = self._list_locked(repo_root=repo_root)
            rows = self._read()
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
            out.write(f"Summary: {active} active, {stale} stale (run with --prune to GC).\n")

            # Stack-params extension table: shows N/X/Y_BASE/Y_VIP/PORT.
            # Only emitted when the registry is non-empty.
            if rows:
                out.write("\n")
                out.write(f"{'BRANCH_DIR':<55}  {'N':>5}  {'X':>3}  {'Y_BASE':>6}  {'Y_VIP':>5}  {'PORT':>5}\n")
                out.write(f"{'----------':<55}  {'-':>5}  {'-':>3}  {'------':>6}  {'-----':>5}  {'----':>5}\n")
                for row in rows:
                    p = row.params
                    out.write(
                        f"{row.branch_dir:<55}  " f"{p.index:>5}  {p.x:>3}  {p.y_base:>6}  {p.y_vip:>5}  {p.port:>5}\n"
                    )

            out.write("\n")
            out.write("Docker networks in 172.[16-31].x.x:\n")
            out.write(f"{'NETWORK':<65}  X\n")
            out.write(f"{'-------':<65}  -\n")
            nets = sorted(self._docker_networks_in_range(), key=lambda n: n.prefix)
            for net in nets:
                out.write(f"{net.name:<65}  {net.prefix}\n")
            out.write("\n")
            out.write(
                f"Free range: 172.16.0.0/12 carved into /23 stacks "
                f"(indices [{INDEX_LO},{INDEX_HI}]). "
                "Used by registry + docker networks listed above are blocked.\n"
            )
            out.write("\n")
            self_ref = script_self or "wt-ctl network"
            out.write("Pruning info:\n")
            out.write(f"  - Stale registry entries:  {self_ref} prune [--dry-run]\n")
            out.write(f"  - Plus orphan docker nets: {self_ref} prune --networks [--dry-run]\n")
            out.write(f"  - Specific entry:          {self_ref} release <branch_dir>\n")
            out.write("  - When you tear down a worktree via wt-ctl delete, the registry\n")
            out.write("    entry is released automatically.\n")
        return out.getvalue()


# ---------------------------------------------------------------------------
# row parsing/formatting
# ---------------------------------------------------------------------------


def _parse_rhs(branch_dir: str, rhs: str) -> Optional[_RegistryRow]:
    """Parse ``<N>:<X>:<Y_BASE>:<Y_VIP>:<PORT>``.

    Returns ``None`` for any malformed line so the registry tolerates a
    user hand-edit. A bare-int row (``<NN>`` alone) is treated as malformed.
    """
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
    derived = stack_params(n)
    if (x, y_base, y_vip, port) != (derived.x, derived.y_base, derived.y_vip, derived.port):
        sys.stderr.write(
            f"[wt-ctl] network: WARN registry row {branch_dir}={rhs} "
            f"is internally inconsistent — using derived values.\n"
        )
        params = derived
    else:
        params = StackParams(index=n, x=x, y_base=y_base, y_vip=y_vip, port=port)
    return _RegistryRow(branch_dir=branch_dir, params=params)


def _format_rhs(p: StackParams) -> str:
    return f"{p.index}:{p.x}:{p.y_base}:{p.y_vip}:{p.port}"


def _format_row(row: _RegistryRow) -> str:
    return f"{row.branch_dir}={_format_rhs(row.params)}"


# ---------------------------------------------------------------------------
# .env helpers
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


# ---------------------------------------------------------------------------
# NetworkDomain — adapter over Registry
# ---------------------------------------------------------------------------

_LIST_HEADER = re.compile(r"^BRANCH_DIR\s+PREFIX\s+STATUS\s+WORKTREE\s*$")
_DOCKER_HEADER = re.compile(r"^Docker networks in 172\.\[")


class NetworkDomain:
    """``repo_root`` is the worktree root from which we derive (a) the
    conventional worktree-parent dir for stale checks and (b) the git repo
    to query ``git worktree list``.
    """

    def __init__(self, runner: Runner, repo_root: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root
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
        return self._registry.render_list(repo_root=self.repo_root, script_self=script_self)

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
        """
        params = self._registry.allocate(
            branch_dir=branch_dir,
            auto_prune=True,
            repo_root=self.repo_root,
        )
        return "\n".join(env_lines_for(params)) + "\n"


# ---------------------------------------------------------------------------
# text parser for bash-formatted registry output stored in test fixtures
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
    """Status-renderer helper. Renders a stack index as ``172.X.Y_BASE.0/23``."""
    if INDEX_LO <= prefix <= INDEX_HI:
        return stack_params(prefix).subnet
    return f"<out-of-range {prefix}>"


def gost_proxy_for(prefix: int) -> str:
    """Map a stack index to the gost-proxy URL (``port = 8000 + index``)."""
    if INDEX_LO <= prefix <= INDEX_HI:
        return f"http://127.0.0.1:{8000 + prefix}"
    return f"<out-of-range {prefix}>"


def namespace_for_prefix(prefix: int, base: str = "mck-devc") -> str:
    """NAMESPACE construction uses the stack index verbatim as a unique
    per-stack suffix.
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
