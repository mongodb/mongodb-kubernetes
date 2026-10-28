"""The kubeconfig files of a worktree: the one the host uses and the copy the devcontainer uses.

``recreate_kind_cluster(s).sh`` write ``.generated/current.kubeconfig`` with servers on the host's loopback.
The container cannot reach those, so ``refresh`` derives ``current.devc.kubeconfig`` from it. The operator's own
``multicluster.devc.kubeconfig``, which ``make prepare-local-e2e`` writes in the container, is left as it is unless
the worktree has an Evergreen host: it names the servers of ``current.devc.kubeconfig`` with each cluster's CA, and the
kind certificate covers those names (see ``kubeadm_cert_sans_patches`` in ``setup_kind_cluster.sh``). The operator needs
that CA: its member cluster health check builds its own TLS client from it.

A worktree with an Evergreen host (``.generated/.current-evg-host``) has loopback servers on the host, and no
rewrite helps. Both sides send kubectl through a proxy that ssh-tunnels to the host: the host file through the
worktree's published gost port, the container's files through the ``gost-proxy`` sidecar.
"""

from __future__ import annotations

import copy
import platform
import re
import tempfile
from pathlib import Path
from typing import Optional

try:
    import yaml
except ImportError:  # a fresh checkout has no venv yet; only the verbs that parse a kubeconfig need it
    yaml = None

from ..errors import WtCtlError
from ..runner import Runner

GOST_PROXY_URL = "http://gost-proxy:8080"  # the sidecar in compose.yml
KIND_API_PORT = 6443
_LOOPBACK = re.compile(r"^https?://(?:127\.0\.0\.1|localhost)(?::(?P<port>\d+))?(?:/|$)")


def need_yaml():
    """PyYAML, or a clear error: the verbs that only run commands work in a checkout without a venv."""
    if yaml is None:
        raise WtCtlError("PyYAML is needed to read kubeconfigs: build the venv (scripts/dev/recreate_python_venv.sh)")
    return yaml


def rewrite_for_devc(kc: dict, node_ips: Optional[dict[str, str]] = None) -> dict:
    """A copy of ``kc`` whose servers the container can reach, without TLS verification.

    ``node_ips`` maps a cluster name to the IP of its kind node on the ``kind`` docker network, which the
    container joins on Linux; the server becomes ``https://<ip>:6443``. Without an IP (macOS) a loopback server
    becomes ``host.docker.internal`` on the same port. Any other server stays. A rewritten cluster loses its CA and
    gets ``insecure-skip-tls-verify``."""
    out = copy.deepcopy(kc)
    for entry in out.get("clusters") or []:
        cluster = entry["cluster"]
        loopback = _LOOPBACK.match(cluster["server"])
        ip = (node_ips or {}).get(entry["name"])
        if ip:
            cluster["server"] = f"https://{ip}:{KIND_API_PORT}"
        elif loopback:
            cluster["server"] = f"https://host.docker.internal:{loopback.group('port') or 443}"
        else:
            continue
        cluster["insecure-skip-tls-verify"] = True
        cluster.pop("certificate-authority-data", None)
        cluster.pop("certificate-authority", None)
    return out


def with_proxy_url(kc: dict, proxy_url: str) -> dict:
    """A copy of ``kc`` whose every cluster is reached through ``proxy_url``."""
    out = copy.deepcopy(kc)
    for entry in out.get("clusters") or []:
        entry["cluster"]["proxy-url"] = proxy_url
    return out


def kind_node_ips(runner: Runner, kc: dict, prefix: str) -> dict[str, str]:
    """Cluster name to node IP for every kind cluster of the worktree that has a node container."""
    ips = {}
    for entry in kc.get("clusters") or []:
        node = f"{prefix}{entry['name'].removeprefix('kind-')}-control-plane"
        res = runner.run(["docker", "inspect", "-f", "{{.NetworkSettings.Networks.kind.IPAddress}}", node], check=False)
        if res.rc == 0 and res.stdout.strip():
            ips[entry["name"]] = res.stdout.strip()
    return ips


def _write(path: Path, kc: dict) -> None:
    """Replace ``path`` in one step: kubectl reads these files while they are rewritten."""
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    with open(fd, "w") as f:
        yaml.safe_dump(kc, f, sort_keys=False)
    Path(tmp).replace(path)


def refresh(runner: Runner, worktree: Path, prefix: str, current_context: str, proxy_port: int) -> None:
    """Pin ``current_context`` in the host kubeconfig and write the container's copies of it. ``proxy_port`` is the
    worktree's published gost port, which the host file of an Evergreen worktree goes through."""
    need_yaml()
    generated = worktree / ".generated"
    host_file = generated / "current.kubeconfig"
    if not host_file.is_file():
        raise WtCtlError(f"{host_file} not found: the kind clusters of the worktree are not created yet")
    evg = (generated / ".current-evg-host").is_file()
    sources = {host_file: generated / "current.devc.kubeconfig"}
    multi_file = generated / "multicluster.devc.kubeconfig"
    if evg and multi_file.is_file():  # only the proxy-url: its servers and CAs are right (see the module docstring)
        sources[multi_file] = multi_file
    for source, target in sources.items():
        kc = yaml.safe_load(source.read_text())
        if source == host_file:
            if current_context in [c["name"] for c in kc.get("contexts") or []]:
                kc["current-context"] = current_context
            if evg:
                kc = with_proxy_url(kc, f"http://127.0.0.1:{proxy_port}")
            _write(source, kc)
        if evg:
            _write(target, with_proxy_url(kc, GOST_PROXY_URL))
        else:
            node_ips = kind_node_ips(runner, kc, prefix) if platform.system() == "Linux" else None
            _write(target, rewrite_for_devc(kc, node_ips))
