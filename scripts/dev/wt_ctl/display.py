"""Rendering. Inputs are dataclasses from ``state.py``; outputs are strings.

ANSI auto: enabled only when stdout is a tty AND ``color != 'never'``.
"""

from __future__ import annotations

import os
import sys
from typing import Iterable, Optional

from .state import GlobalStatus, OrphanRegistration, WorktreeRow, WorktreeStatus


class _Ansi:
    RESET = "\033[0m"
    BOLD = "\033[1m"
    DIM = "\033[2m"
    GREEN = "\033[32m"
    YELLOW = "\033[33m"
    RED = "\033[31m"
    CYAN = "\033[36m"
    GREY = "\033[90m"


def _color_enabled(mode: str) -> bool:
    if mode == "always":
        return True
    if mode == "never":
        return False
    # "auto"
    if not sys.stdout.isatty():
        return False
    if os.environ.get("NO_COLOR"):
        return False
    return True


def _wrap(s: str, code: str, enabled: bool) -> str:
    if not enabled or not s:
        return s
    return f"{code}{s}{_Ansi.RESET}"


class KVTable:
    """Right-pad keys to a common width, emit ``key   value`` lines."""

    def __init__(self, indent: str = "") -> None:
        self.rows: list[tuple[str, str]] = []
        self.indent = indent

    def add(self, key: str, value: str) -> None:
        self.rows.append((key, value))

    def blank(self) -> None:
        self.rows.append(("", ""))

    def render(self) -> str:
        width = max((len(k) for k, _ in self.rows if k), default=0)
        out: list[str] = []
        for key, value in self.rows:
            if not key and not value:
                out.append("")
                continue
            if not key:
                out.append(f"{self.indent}{' ' * width}  {value}")
            else:
                out.append(f"{self.indent}{key.ljust(width)}  {value}")
        return "\n".join(out)


def _git_summary(status: WorktreeStatus) -> str:
    g = status.git
    clean = "clean" if g.clean else "dirty"
    parts = [clean]
    if g.upstream:
        parts.append(f"{g.ahead} ahead/{g.behind} behind {g.upstream}")
    return f"{status.worktree_dir}  ({', '.join(parts)})"


def render_status(status: WorktreeStatus, *, color: str = "auto") -> str:
    enabled = _color_enabled(color)
    t = KVTable()
    t.add("worktree", _git_summary(status))
    t.add("path", status.worktree_path)
    if status.context:
        t.add("context", status.context)
    t.blank()

    if status.devc:
        d = status.devc
        if d.state == "running":
            state_str = _wrap(d.state, _Ansi.GREEN, enabled)
        elif d.state in ("exited", "dead"):
            state_str = _wrap(d.state, _Ansi.RED, enabled)
        else:
            state_str = _wrap(d.state, _Ansi.GREY, enabled)
        details = f"project={d.project}"
        details += f"  services={d.services_running}/{d.services_total}"
        if d.image_age:
            details += f"  image_age={d.image_age}"
        t.add("devc", f"{state_str}    {details}")
    else:
        t.add("devc", _wrap("-", _Ansi.GREY, enabled) + "    no compose stack found")

    n = status.network
    if n.prefix is not None:
        net_line = (
            f"{n.subnet}    prefix={n.prefix}    "
            f"registered={'yes' if n.registered else 'no'}    "
            f"orphans={n.orphans}"
        )
        t.add("network", net_line)
    else:
        t.add("network", "(no prefix allocated)")

    if n.namespace:
        t.add("namespace", n.namespace)
    if n.gost_proxy:
        t.add("gost-proxy", n.gost_proxy)

    t.blank()

    if status.evg is not None:
        e = status.evg
        if e.status == "running":
            ev_state = _wrap(e.status or "-", _Ansi.GREEN, enabled)
        elif e.status in (None, "terminated", "terminating", "decommissioned"):
            ev_state = _wrap(e.status or "-", _Ansi.GREY, enabled)
        else:
            ev_state = _wrap(e.status or "-", _Ansi.YELLOW, enabled)
        ev_line = ev_state
        if e.name:
            ev_line += f"    name={e.name}"
        if e.id:
            ev_line += f"    id={e.id}"
        if e.expires_in:
            ev_line += f"    expires_in={e.expires_in}"
        t.add("evg host", ev_line)
        if e.ssh:
            t.add("ssh", e.ssh)
    else:
        t.add("evg host", _wrap("-", _Ansi.GREY, enabled) + "    no EVG_HOST_NAME pinned")

    if status.kubeconfig is not None:
        kc = status.kubeconfig
        line = (
            f"{kc.path or '-'}    "
            f"last_patch={kc.last_patch or '-'}    "
            f"kfp_registered={'yes' if kc.kfp_registered else 'no'}"
        )
        t.add("kubeconfig", line)

    if status.kfp is not None:
        k = status.kfp
        if k.listening:
            state_str = _wrap("running", _Ansi.GREEN, enabled)
            health_part = f"health={k.health or 'unreachable'}"
            t.add(
                "host-kfp",
                f"{state_str}    http={k.http_endpoint}    {health_part}",
            )
        else:
            state_str = _wrap("not_running", _Ansi.YELLOW, enabled)
            t.add(
                "host-kfp",
                f"{state_str}    (pkg-installed, launchd-managed; check `launchctl print system/io.github.fealebenpae.k8s-proxy`)",
            )

    if status.om is not None:
        om = status.om
        count = om.project_count if om.project_count is not None else "?"
        scope = om.scope or "(unknown scope)"
        t.add("om", f"{count} projects in scope `{scope}`")

    if status.next_hints:
        t.blank()
        first, *rest = status.next_hints
        t.add("next", first)
        for hint in rest:
            t.add("", hint)

    return t.render()


_HEADERS = ("WORKTREE", "BRANCH", "NET", "NAMESPACE", "DEVC", "EVG-NAME", "EXPIRES")


def _row_cells(row: WorktreeRow) -> tuple[str, ...]:
    return (
        row.worktree,
        row.branch or "-",
        str(row.prefix) if row.prefix is not None else "-",
        row.namespace or "-",
        row.devc_state or "-",
        row.evg_name or "-",
        row.evg_expires or row.evg_status or "-",
    )


def render_status_all(g: GlobalStatus, *, color: str = "auto") -> str:
    enabled = _color_enabled(color)
    cells = [_HEADERS] + [_row_cells(r) for r in g.rows]
    widths = [max(len(c) for c in column) for column in zip(*cells)]
    out: list[str] = []
    out.append("  ".join(c.ljust(w) for c, w in zip(_HEADERS, widths)))
    for row in g.rows:
        line = "  ".join(c.ljust(w) for c, w in zip(_row_cells(row), widths))
        if row.prunable:
            line = _wrap(line, _Ansi.YELLOW, enabled)
        out.append(line)

    prunable = [r for r in g.rows if r.prunable]
    if prunable:
        out.append("")
        out.append("prunable")
        name_w = max(len(r.worktree) for r in prunable)
        for r in prunable:
            reason = r.prunable_reason or "?"
            cmd = r.delete_cmd or "-"
            out.append(f"  {r.worktree.ljust(name_w)}  reason={reason}  ->  {cmd}")

    if g.orphans:
        out.append("")
        out.append("orphan registrations")
        name_w = max(len(o.branch_dir) for o in g.orphans)
        for o in g.orphans:
            out.append(f"  {o.branch_dir.ljust(name_w)}  prefix={o.prefix}  ->  {o.release_cmd}")

    return "\n".join(out)


def render_kv(pairs: Iterable[tuple[str, str]]) -> str:
    t = KVTable()
    for k, v in pairs:
        t.add(k, v)
    return t.render()
