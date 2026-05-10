"""Network prefix domain — **native Python registry** (Phase 2.5).

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
                                       ``MCK_DEVC_NET_PREFIX=<NN>\n``)

Storage layout (unchanged from bash):

  - ``${MCK_DEVC_REGISTRY_DIR:-~/.cache/mck-devc}/net-prefix-registry``
  - One line per allocation: ``<branch_dir>=<prefix>`` (no timestamp; the
    plan's "iso8601 ts" turned out to be aspirational — keep parity with
    the on-disk format the bash script wrote so we can round-trip).
  - Lock dir: ``<registry_dir>/net-prefix-registry.lock.d`` (mkdir-based,
    portable across macOS/Linux). Stale-lock detection: if the lock dir's
    pidfile holds a dead PID OR the lock dir mtime is older than
    ``LOCK_STALE_SECS``, force-release with a stderr warning.

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


VALID_RANGE_LO = 16
VALID_RANGE_HI = 31

LOCK_TIMEOUT_SECS = 30          # max wait to acquire a lock
LOCK_POLL_INTERVAL_SECS = 0.1
LOCK_STALE_SECS = 60            # force-release a lock dir older than this


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
    def _read(self) -> list[tuple[str, int]]:
        _, rfile = _ensure_registry()
        out: list[tuple[str, int]] = []
        try:
            text = rfile.read_text()
        except OSError as exc:
            raise RegistryError(f"failed to read {rfile}: {exc}") from exc
        for raw in text.splitlines():
            line = raw.strip()
            if not line:
                continue
            if "=" not in line:
                continue
            bd, rhs = line.split("=", 1)
            bd = bd.strip()
            rhs = rhs.strip()
            if not bd or not rhs:
                continue
            try:
                pref = int(rhs)
            except ValueError:
                continue
            out.append((bd, pref))
        return out

    def _write(self, rows: list[tuple[str, int]]) -> None:
        _, rfile = _ensure_registry()
        body = "".join(f"{bd}={pref}\n" for bd, pref in rows)
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
            if VALID_RANGE_LO <= x <= VALID_RANGE_HI:
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
        for bd, pref in rows:
            if self._is_stale_branch_dir(bd, git_basenames=git_names):
                entries.append(
                    NetEntry(
                        branch_dir=bd,
                        prefix=pref,
                        status="stale",
                        worktree_path=None,
                    )
                )
            else:
                entries.append(
                    NetEntry(
                        branch_dir=bd,
                        prefix=pref,
                        status="active",
                        worktree_path=str(self.worktree_parent / bd),
                    )
                )
        return entries, []

    def release(self, branch_dir: str, *, dry_run: bool = False) -> str:
        out = StringIO()
        with _registry_lock():
            rows = self._read()
            match = next((p for bd, p in rows if bd == branch_dir), None)
            if match is None:
                out.write(f"No registry entry for {branch_dir}; nothing to release.\n")
                return out.getvalue()
            if dry_run:
                out.write(f"[dry-run] would release {branch_dir}={match}\n")
                return out.getvalue()
            kept = [(bd, p) for bd, p in rows if bd != branch_dir]
            self._write(kept)
            out.write(f"Released {branch_dir}={match}.\n")
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
            kept: list[tuple[str, int]] = []
            removed = 0
            out.write("Scanning registry for stale entries…\n")
            for bd, pref in rows:
                if self._is_stale_branch_dir(bd, git_basenames=git_names):
                    if dry_run:
                        out.write(f"[dry-run] stale: {bd}={pref}\n")
                    else:
                        out.write(f"Pruning stale registry entry: {bd}={pref}\n")
                    removed += 1
                else:
                    kept.append((bd, pref))
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
                 emit_warning: Optional[callable] = None) -> int:
        """Allocate (or return existing) prefix for ``branch_dir``.

        Returns the integer prefix. The CLI wrapper formats it as the
        ``MCK_DEVC_NET_PREFIX=<NN>`` line that callers grep for.

        ``MCK_DEVC_NET_PREFIX`` env override is honored — caller takes
        ownership and we skip both the registry and the docker scan.
        """
        env_pref = os.environ.get("MCK_DEVC_NET_PREFIX")
        if env_pref:
            if env_pref.isdigit():
                v = int(env_pref)
                if VALID_RANGE_LO <= v <= VALID_RANGE_HI:
                    return v
            raise RegistryError(
                f"MCK_DEVC_NET_PREFIX='{env_pref}' is not in [{VALID_RANGE_LO},{VALID_RANGE_HI}]"
            )

        warn = emit_warning if emit_warning is not None else (lambda m: sys.stderr.write(m + "\n"))

        with _registry_lock(warn=warn):
            rows = self._read()

            # Idempotent: branch already has a prefix?
            if branch_dir:
                for bd, p in rows:
                    if bd == branch_dir:
                        return p

            # Auto-prune stale entries.
            if auto_prune and rows:
                git_names = self._git_worktree_basenames(repo_root) if repo_root else set()
                kept: list[tuple[str, int]] = []
                pruned = 0
                for bd, p in rows:
                    if self._is_stale_branch_dir(bd, git_basenames=git_names):
                        warn(f"Auto-pruning stale registry entry: {bd}={p}")
                        pruned += 1
                    else:
                        kept.append((bd, p))
                if pruned > 0:
                    self._write(kept)
                    rows = kept

            # Used = registry prefixes + live docker networks in range.
            used: set[int] = {p for _, p in rows}
            for net in self._docker_networks_in_range():
                used.add(net.prefix)

            for cand in range(VALID_RANGE_LO, VALID_RANGE_HI + 1):
                if cand in used:
                    continue
                if branch_dir:
                    rows.append((branch_dir, cand))
                    self._write(rows)
                return cand

            raise RegistryError(
                f"no free 172.[{VALID_RANGE_LO}-{VALID_RANGE_HI}].0.0/16 subnet available "
                f"(used: {sorted(used)})"
            )

    # ------------------------------------------------------------------
    # rendering — must byte-match the bash --list output (modulo dynamic
    # `Worktree parent` line, which depends on caller cwd)
    # ------------------------------------------------------------------
    def render_list(self, *, repo_root: Optional[Path] = None,
                    script_self: Optional[str] = None) -> str:
        """Return the full ``--list`` text body (registry table +
        summary + docker networks + pruning hints). Byte-compatible with
        the legacy bash output.
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
            out.write("\n")
            out.write(
                f"Docker networks in 172.[{VALID_RANGE_LO}-{VALID_RANGE_HI}].0.0/16:\n"
            )
            out.write(f"{'NETWORK':<65}  PREFIX\n")
            out.write(f"{'-------':<65}  ------\n")
            nets = sorted(self._docker_networks_in_range(), key=lambda n: n.prefix)
            for net in nets:
                out.write(f"{net.name:<65}  {net.prefix}\n")
            out.write("\n")
            out.write(
                f"Free range: 172.[{VALID_RANGE_LO}-{VALID_RANGE_HI}].0.0/16.\n"
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
        prefix = self._registry.allocate(
            branch_dir=branch_dir,
            auto_prune=True,
            repo_root=self.repo_root,
        )
        return f"MCK_DEVC_NET_PREFIX={prefix}\n"


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
    return f"172.{prefix}.0.0/16"


def gost_proxy_for(prefix: int) -> str:
    return f"http://127.0.0.1:80{prefix:02d}"


def namespace_for_prefix(prefix: int, base: str = "mck-devc") -> str:
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
