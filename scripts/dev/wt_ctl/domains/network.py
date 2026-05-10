"""Network prefix domain. Wraps ``scripts/dev/dc_select_network.sh``.

Phase 1 deliverables: list / prune / release / allocate. Parsing the line-
based ``--list`` output gives us per-worktree NetEntry rows (mirrors the
table the script prints).
"""

from __future__ import annotations

import re
from pathlib import Path
from typing import Optional

from ..errors import ExternalCommandFailed, RegistryError
from ..runner import Runner
from ..state import NetEntry


_LIST_HEADER = re.compile(r"^BRANCH_DIR\s+PREFIX\s+STATUS\s+WORKTREE\s*$")
_DOCKER_HEADER = re.compile(r"^Docker networks in 172\.\[")


class NetworkDomain:
    def __init__(self, runner: Runner, repo_root: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root
        self.script = repo_root / "scripts/dev/dc_select_network.sh"

    # ------------------------------------------------------------------
    def list_entries(self) -> list[NetEntry]:
        """Parse ``dc_select_network.sh --list``."""
        res = self.runner.run([str(self.script), "--list"])
        return _parse_list_output(res.stdout)

    def list_raw(self) -> str:
        return self.runner.run([str(self.script), "--list"]).stdout

    # ------------------------------------------------------------------
    def prefix_for(self, branch_dir: str) -> Optional[int]:
        for entry in self.list_entries():
            if entry.branch_dir == branch_dir:
                return entry.prefix
        return None

    # ------------------------------------------------------------------
    def prune(self, *, dry_run: bool = False) -> str:
        argv = [str(self.script), "--prune"]
        if dry_run:
            argv.append("--dry-run")
        return self.runner.run(argv).stdout

    def release(self, branch_dir: str) -> str:
        return self.runner.run(
            [str(self.script), "--release", branch_dir],
        ).stdout

    def allocate(self, branch_dir: Optional[str] = None) -> str:
        argv = [str(self.script)]
        if branch_dir:
            argv += ["--branch-dir", branch_dir]
        return self.runner.run(argv).stdout


def _parse_list_output(text: str) -> list[NetEntry]:
    out: list[NetEntry] = []
    in_table = False
    for line in text.splitlines():
        if _LIST_HEADER.match(line):
            in_table = True
            continue
        if not in_table:
            continue
        # Stop at the next blank line or summary section.
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
        # Columns: BRANCH_DIR  PREFIX  STATUS  WORKTREE
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


def subnet_for(prefix: int) -> str:
    return f"172.{prefix}.0.0/16"


def gost_proxy_for(prefix: int) -> str:
    return f"http://127.0.0.1:80{prefix:02d}"


def namespace_for_prefix(prefix: int, base: str = "mck-devc") -> str:
    """Default scoped namespace, used when no NAMESPACE override is detected
    in private-context. Pattern from root-context: ``${NAMESPACE}-${prefix}``.
    The final NAMESPACE depends on private-context; we surface the documented
    pattern for display until we can read the rendered context.env.
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
