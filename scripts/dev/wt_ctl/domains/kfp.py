"""The kube-forwarding-proxy (kfp) daemon on the host.

On a Mac the daemon is a launchd service installed from its pkg: an HTTP control port on ``127.0.0.1:11616`` and a DNS
server that resolves ``*.svc.cluster.local`` for the kubeconfigs registered with it. ``wt-ctl`` never installs,
starts or stops it. It registers a worktree's host kubeconfig and removes it again, from the host or, through
``host.docker.internal``, from the devcontainer. A Linux host has no daemon, and every call is then a no-op.

One daemon serves every worktree, so the names in a registered kubeconfig get the worktree's proxy port as a suffix
(``kind-kind-8005``). The file on disk keeps its names.
"""

from __future__ import annotations

import copy
import os
import platform
import urllib.error
import urllib.request
from pathlib import Path
from typing import Optional

from ..envfile import inside_container
from ..errors import WtCtlError
from .kubeconfig import need_yaml
from .network import read_pin

HOST_URL = "http://127.0.0.1:11616"
CONTAINER_URL = "http://host.docker.internal:11616"
KICKSTART = "sudo launchctl kickstart -k system/io.github.fealebenpae.k8s-proxy"


def suffix_names(kc: dict, port: int) -> dict:
    """A copy of ``kc`` with ``-<port>`` after every cluster, user and context name, and the references to them."""
    out = copy.deepcopy(kc)

    def rename(name: str) -> str:
        return f"{name}-{port}"

    for key in ("clusters", "users", "contexts"):
        for entry in out.get(key) or []:
            entry["name"] = rename(entry["name"])
    for entry in out.get("contexts") or []:
        for ref in ("cluster", "user"):
            if ref in entry["context"]:
                entry["context"][ref] = rename(entry["context"][ref])
    if out.get("current-context"):
        out["current-context"] = rename(out["current-context"])
    return out


def daemon_url() -> Optional[str]:
    """Where this process reaches the daemon, None when the host cannot run it. The container sees its own Linux,
    so ``initialize.py`` passes the host OS in ``MCK_DEVC_HOST_OS``; a container without it counts as a Mac."""
    host_os = (os.environ.get("MCK_DEVC_HOST_OS") if inside_container() else platform.system()) or "Darwin"
    if host_os != "Darwin":
        return None
    return CONTAINER_URL if inside_container() else HOST_URL


def _request(method: str, url: str, data: Optional[bytes] = None) -> bytes:
    req = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/yaml"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.read()
    except (urllib.error.URLError, OSError) as exc:
        raise WtCtlError(
            f"kfp {method} {url} failed: {exc}. If the daemon is installed, start it with: {KICKSTART}"
        ) from exc


def is_running(url: str) -> bool:
    try:
        _request("GET", f"{url}/healthz")
    except WtCtlError:
        return False
    return True


def _registered(url: str) -> dict:
    """What the HTTP API registered so far, never the daemon's own kubeconfig file."""
    return need_yaml().safe_load(_request("GET", f"{url}/kubeconfig")) or {}


def contexts(url: str) -> list[str]:
    return sorted(entry["name"] for entry in _registered(url).get("contexts") or [])


def register_worktree(wt: Path) -> None:
    """Send the worktree's host kubeconfig to the daemon."""
    url, pin = daemon_url(), read_pin(wt)
    if url is None:
        return
    if pin is None:
        raise WtCtlError(f"{wt} has no worktree index: it was not created by wt-ctl")
    kubeconfig = wt / ".generated" / "current.kubeconfig"
    if not kubeconfig.is_file():
        raise WtCtlError(f"{kubeconfig} not found: the kind clusters of the worktree are not created yet")
    yaml = need_yaml()
    body = yaml.safe_dump(suffix_names(yaml.safe_load(kubeconfig.read_text()), pin.port), sort_keys=False)
    _request("PATCH", f"{url}/kubeconfig", body.encode())


def unregister_worktree(wt: Path) -> list[str]:
    """Remove the worktree's entries, the names that end in its proxy port, and return its context names.

    The daemon has no delete for one context, so this reads its registrations and puts the rest back (or deletes
    them all when none is left). A registration that lands between the read and the write is lost; ``wt-ctl
    kubeconfig refresh`` sends it again."""
    url, pin = daemon_url(), read_pin(wt)
    if url is None or pin is None:
        return []
    suffix = f"-{pin.port}"
    current = _registered(url)
    removed = [entry["name"] for entry in current.get("contexts") or [] if entry["name"].endswith(suffix)]
    if not removed:
        return []
    for key in ("clusters", "users", "contexts"):
        current[key] = [entry for entry in current.get(key) or [] if not entry["name"].endswith(suffix)]
    if str(current.get("current-context") or "").endswith(suffix):
        current["current-context"] = ""
    if any(current[key] for key in ("clusters", "users", "contexts")):
        _request("PUT", f"{url}/kubeconfig", need_yaml().safe_dump(current, sort_keys=False).encode())
    else:
        _request("DELETE", f"{url}/kubeconfig")
    return removed
