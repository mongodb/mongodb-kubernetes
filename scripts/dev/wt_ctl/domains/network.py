"""The worktree index and what is computed from it.

Index ``N`` (0 to 126) belongs to one checkout of the repository. It names the cluster prefix ``devc<N>-``, the
third octet ``128 + N`` of the MetalLB pools on the shared ``kind`` network, and the compose network of the
devcontainer. The checkouts are the only record: ``N`` is pinned in ``<checkout>/.devcontainer/.env``, and removing
the checkout frees it.

Compose cannot call code, so the compose values (``MCK_DEVC_NET_*``) go to ``.devcontainer/.env`` and compose does
the arithmetic of its own: a ``/23`` per worktree in ``172.16.0.0/16``, containers in the bottom ``/24`` and the
k8s-proxy addresses in the top one. Everything bash and pytest need comes from ``worktree-context`` as literals.
"""

from __future__ import annotations

import fcntl
import ipaddress
import os
import re
import tempfile
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator, Optional

from ..envfile import read_env_file
from ..errors import ToolMissing, WorktreeIndexError, WtCtlError
from ..runner import Runner
from .worktree import WorktreeDomain

# MetalLB rows 128 + N must fit in a third octet below 255.
INDEX_HI = 126
METALLB_ROW_BASE = 128
LOCK_NAME = "net-alloc.lock"
PIN_KEYS = (
    "MCK_DEVC_INDEX",
    "MCK_DEVC_NET_X",
    "MCK_DEVC_NET_Y_BASE",
    "MCK_DEVC_NET_Y_VIP",
    "MCK_DEVC_PROXY_PORT",
)


@dataclass(frozen=True)
class WorktreeParams:
    index: int
    x: int
    y_base: int
    y_vip: int
    port: int


def worktree_params(index: int) -> WorktreeParams:
    """The compose network and the gost-proxy port of worktree ``index``."""
    if not 0 <= index <= INDEX_HI:
        raise WorktreeIndexError(
            f"worktree index {index} is outside 0..{INDEX_HI}: a worktree needs its own MetalLB row"
        )
    y_base = (index & 0x7F) << 1
    return WorktreeParams(index, 16 + (index >> 7), y_base, y_base + 1, 8000 + index)


def compose_subnet(params: WorktreeParams) -> ipaddress.IPv4Network:
    """The ``/23`` of the worktree's compose network, as the ``networks`` block of compose.yml builds it."""
    return ipaddress.ip_network(f"172.{params.x}.{params.y_base}.0/23")


def parse_subnets(text: str) -> list[ipaddress.IPv4Network]:
    """The IPv4 subnets in whitespace separated ``docker network inspect`` output; anything else is skipped."""
    subnets = []
    for word in text.split():
        try:
            net = ipaddress.ip_network(word, strict=False)
        except ValueError:
            continue
        if isinstance(net, ipaddress.IPv4Network):
            subnets.append(net)
    return subnets


def env_lines_for(params: WorktreeParams) -> list[str]:
    values = (params.index, params.x, params.y_base, params.y_vip, params.port)
    return [f"{key}={value}" for key, value in zip(PIN_KEYS, values)]


def read_pin(worktree: Path) -> Optional[WorktreeParams]:
    """The worktree index pinned in ``<worktree>/.devcontainer/.env``, if any."""
    value = read_env_file(worktree / ".devcontainer" / ".env").get("MCK_DEVC_INDEX", "")
    if not value.isdigit() or int(value) > INDEX_HI:
        return None
    return worktree_params(int(value))


def write_pin(env_file: Path, params: WorktreeParams) -> None:
    """Put the worktree's lines into ``env_file`` and keep every other line of it."""
    kept = []
    if env_file.is_file():
        kept = [ln for ln in env_file.read_text().splitlines() if ln.split("=", 1)[0] not in PIN_KEYS]
    env_file.parent.mkdir(parents=True, exist_ok=True)
    env_file.write_text("\n".join(kept + env_lines_for(params)) + "\n")


def worktree_prefix(index: int) -> str:
    return f"devc{index}-"


def worktree_values(index: int, root_context_text: str) -> dict[str, str]:
    """The prefix and the six MetalLB pools of worktree ``index`` as ``{variable: value}``.

    The pools are root-context's default ones with the third octet set to ``128 + index``, so the layout of the
    address plan stays in root-context."""
    worktree_params(index)  # raises for an index without a row
    row = METALLB_ROW_BASE + index
    values = {"WORKTREE_PREFIX": worktree_prefix(index)}
    for name, pool in re.findall(r'^export (KIND_METALLB_RANGE_\w+)="([^"]+)"', root_context_text, re.MULTILINE):
        values[name] = re.sub(r"(\d+\.\d+)\.\d+\.(\d+)", rf"\1.{row}.\2", pool)
    if len(values) != 7:
        raise WorktreeIndexError(f"expected six KIND_METALLB_RANGE_* lines in root-context, found {len(values) - 1}")
    return values


def write_worktree_context(path: Path, values: dict[str, str], evg_host_name: str = "") -> None:
    """Write ``.generated/worktree-context``: ``export NAME="literal"`` lines that root-context sources."""
    if evg_host_name:
        values = {**values, "EVG_HOST_NAME": evg_host_name}
    # root-context sources the file, and a branch name can hold quotes, $ and backticks.
    unsafe = [v for v in values.values() if not re.fullmatch(r"[\w.:@+,=/-]*", v)]
    if unsafe:
        raise WtCtlError(
            f"{unsafe[0]!r} has characters that do not belong in the worktree-context file: use a plainer name"
        )
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".worktree-context.")
    with os.fdopen(fd, "w") as f:
        f.write("# Written by wt-ctl, do not edit.\n")
        f.writelines(f'export {name}="{value}"\n' for name, value in values.items())
    os.replace(tmp, path)


@contextmanager
def flock(path: Path) -> Iterator[None]:
    """Hold an exclusive lock on ``path``, created when missing, for the with block."""
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_CLOEXEC, 0o644)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        yield
    finally:
        os.close(fd)


class NetworkDomain:
    """Allocation of worktree indexes among the worktrees of one repository."""

    def __init__(self, runner: Runner, repo_root: Path, *, lock_dir: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root
        self.lock_dir = lock_dir

    def allocation_lock(self):
        """Hold this from ``allocate`` until the pin is written, so two creates never pick one index."""
        return flock(self.lock_dir / LOCK_NAME)

    def pins(self) -> dict[Path, WorktreeParams]:
        """The worktree index of every worktree that has one."""
        pins = {}
        for entry in WorktreeDomain(self.runner).list_worktrees(self.repo_root):
            pin = entry.path.is_dir() and read_pin(entry.path)
            if pin:
                pins[entry.path.resolve()] = pin
        return pins

    def docker_subnets(self) -> list[ipaddress.IPv4Network]:
        """The subnets of every docker network on the daemon; none when docker does not answer."""
        try:
            ids = self.runner.run(["docker", "network", "ls", "-q"], check=False).stdout.split()
            if not ids:
                return []
            fmt = "{{range .IPAM.Config}}{{.Subnet}} {{end}}"
            return parse_subnets(self.runner.run(["docker", "network", "inspect", "-f", fmt, *ids], check=False).stdout)
        except ToolMissing:
            return []

    def allocate(self, own_worktree: Path) -> WorktreeParams:
        """The pin of ``own_worktree``, or the lowest index that no other worktree pins and whose compose network
        does not overlap a docker network that exists (compose fails with "Pool overlaps" otherwise)."""
        pins = self.pins()
        own = pins.get(own_worktree.resolve())
        if own is not None:
            return own
        taken = {pin.index for pin in pins.values()}
        subnets = self.docker_subnets()

        def usable(n: int) -> bool:
            return n not in taken and not any(compose_subnet(worktree_params(n)).overlaps(net) for net in subnets)

        free = next((n for n in range(INDEX_HI + 1) if usable(n)), None)
        if free is None:
            raise WorktreeIndexError(
                f"no free worktree index in 0..{INDEX_HI}: delete a worktree, or the docker networks that overlap"
            )
        return worktree_params(free)
