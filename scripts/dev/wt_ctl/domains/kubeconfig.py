"""Kubeconfig domain — read summary, post-process per-side variants, register with kfp.

`state_for` and `_age` back ``wt-ctl status``.

`refresh` post-processes the kubeconfig (pyyaml parsing, stdlib urllib for the
kfp PATCH, unit-testable). Dispatch:
   .current-evg-host present → EVG-host: host file gets
       proxy-url=http://127.0.0.1:<PROXY_PORT>; devc variant ${EVG_HOST_PROXY}.
   host server is loopback   → local-kind: rewrite the devc variant's server so
       the devcontainer reaches the kind apiserver (insecure-skip-tls-verify, CA
       stripped). macOS: https://host.docker.internal:<hostport>. Linux:
       host.docker.internal can't reach a 127.0.0.1 host port, so the devc +
       k8s-proxy containers join the `kind` network (dc_up) and target the node
       container IP at :6443; resolving that IP needs host-side docker, so the
       in-devc refresh reuses the host-written server.
   else                      → BYOC: identity-copy host kubeconfig to the devc variant.
Both modes pin `current-context` to ${CLUSTER_NAME} and PATCH the per-side
variant to ${K8S_FWD_PROXY}/kubeconfig. The multi-cluster kubeconfig gets the
same split (``multicluster_kubeconfig`` host / ``multicluster.devc.kubeconfig``
devc), selected by ${KUBE_CONFIG_PATH}.

Name disambiguation: the shared on-host kfp daemon is multi-tenant, so when
MCK_DEVC_PROXY_PORT is set the bytes PATCHed to it (only) get every
cluster/context/user name suffixed with ``-<port>``; on-disk files stay bare.
Daemon identity becomes ``(name, port)`` — re-PATCHing a worktree is idempotent
and peers keep their own entries. On-disk stays bare because every other
consumer (variant context files, ``bin/reset``, host-shell kubectl) expects
bare names.
"""

from __future__ import annotations

import copy
import os
import platform
import re
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from ..errors import ToolMissing, WtCtlError
from ..runner import Runner
from ..state import KubeconfigState


def _import_yaml() -> Any:
    """Lazy import so ``wt-ctl <other-verb>`` doesn't crash on the import
    when pyyaml isn't installed (e.g. early worktree_init before the venv
    is built). refresh() raises a friendly error if the dep is missing.
    """
    try:
        import yaml  # type: ignore
    except ImportError as exc:
        raise WtCtlError(
            "wt-ctl kubeconfig refresh requires pyyaml; install it into the "
            "worktree venv or pip install pyyaml into the python that wt-ctl "
            "is using."
        ) from exc
    return yaml


_LOOPBACK_SERVER_RE = re.compile(r"^https?://(?:127\.0\.0\.1|localhost)(?::(?P<port>\d+))?(?:/|$)")
_KFP_PATCH_TIMEOUT_S = 5.0


def _age(path: Path) -> Optional[str]:
    if not path.is_file():
        return None
    try:
        mtime = datetime.fromtimestamp(path.stat().st_mtime, tz=timezone.utc)
    except OSError:
        return None
    delta = datetime.now(timezone.utc) - mtime
    secs = int(delta.total_seconds())
    if secs < 60:
        return f"{secs}s_ago"
    if secs < 3600:
        return f"{secs // 60}m_ago"
    if secs < 86400:
        return f"{secs // 3600}h_ago"
    return f"{secs // 86400}d_ago"


def _load_yaml(path: Path) -> dict[str, Any]:
    yaml = _import_yaml()
    text = path.read_text()
    try:
        data = yaml.safe_load(text)
    except yaml.YAMLError as exc:
        raise WtCtlError(f"failed to parse {path}: {exc}") from exc
    if not isinstance(data, dict):
        raise WtCtlError(f"{path} did not parse as a YAML mapping")
    return data


def _dump_yaml(path: Path, data: dict[str, Any]) -> None:
    yaml = _import_yaml()
    body = yaml.safe_dump(data, sort_keys=False)
    # Atomic replace: kubectl / other tools actively read these files while we
    # rewrite them, so write a tempfile sibling then os.replace to avoid handing
    # a reader a transient empty/partial YAML document.
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w") as fh:
            fh.write(body)
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, str(path))
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def _first_server(kc: dict[str, Any]) -> Optional[str]:
    clusters = kc.get("clusters")
    if not isinstance(clusters, list) or not clusters:
        return None
    entry = clusters[0]
    if not isinstance(entry, dict):
        return None
    inner = entry.get("cluster")
    if not isinstance(inner, dict):
        return None
    server = inner.get("server")
    return server if isinstance(server, str) else None


def _set_proxy_url(kc: dict[str, Any], proxy_url: Optional[str]) -> None:
    """Set or strip clusters[*].cluster.proxy-url across every cluster entry."""
    for entry in kc.get("clusters") or []:
        inner = entry.get("cluster") if isinstance(entry, dict) else None
        if not isinstance(inner, dict):
            continue
        if proxy_url is None:
            inner.pop("proxy-url", None)
        else:
            inner["proxy-url"] = proxy_url


def _strip_kubeconfig_suffix(kc: dict[str, Any], port: str) -> None:
    """Inverse of ``_suffix_kubeconfig_names``: strip the ``-<port>`` tail
    from every cluster/context/user name (plus cross-refs and
    ``current-context``). Idempotent; entries without the suffix are
    left alone.

    refresh() calls this on the loaded host kubeconfig before any other
    mutation so the downstream deep-copy snapshot for the devc variant
    sees bare names. Without the strip, a second refresh against an
    already-suffixed on-disk file would propagate the suffix into the
    devc kubeconfig (the in-container k8s-proxy sidecar would then
    register under the suffixed name — the opposite of what we want).
    """
    sfx = f"-{port}"

    def _strip(name: Any) -> Any:
        if isinstance(name, str) and name.endswith(sfx) and len(name) > len(sfx):
            return name[: -len(sfx)]
        return name

    for key in ("clusters", "contexts", "users"):
        for entry in kc.get(key) or []:
            if isinstance(entry, dict) and "name" in entry:
                entry["name"] = _strip(entry["name"])

    for entry in kc.get("contexts") or []:
        inner = entry.get("context") if isinstance(entry, dict) else None
        if not isinstance(inner, dict):
            continue
        if "cluster" in inner:
            inner["cluster"] = _strip(inner["cluster"])
        if "user" in inner:
            inner["user"] = _strip(inner["user"])

    cur = kc.get("current-context")
    if isinstance(cur, str):
        kc["current-context"] = _strip(cur)


def _suffix_kubeconfig_names(kc: dict[str, Any], port: str) -> None:
    """Rename every cluster/context/user in ``kc`` to ``<original>-<port>``,
    fix the cross-references in ``contexts[*].context.{cluster,user}``, and
    rewrite ``current-context`` to the suffixed form.

    Identity becomes ``(name, port)`` so peer worktrees don't collide on bare
    names. Skips entries whose name already ends with the suffix so re-runs of
    refresh() on an already-suffixed file are no-ops (idempotent).
    """
    sfx = f"-{port}"

    def _rename(name: Any) -> Any:
        if not isinstance(name, str) or name.endswith(sfx):
            return name
        return f"{name}{sfx}"

    for key in ("clusters", "contexts", "users"):
        for entry in kc.get(key) or []:
            if isinstance(entry, dict) and "name" in entry:
                entry["name"] = _rename(entry["name"])

    for entry in kc.get("contexts") or []:
        inner = entry.get("context") if isinstance(entry, dict) else None
        if not isinstance(inner, dict):
            continue
        if "cluster" in inner:
            inner["cluster"] = _rename(inner["cluster"])
        if "user" in inner:
            inner["user"] = _rename(inner["user"])

    cur = kc.get("current-context")
    if isinstance(cur, str) and cur and not cur.endswith(sfx):
        kc["current-context"] = f"{cur}{sfx}"


def _kind_node_ip(runner: Runner, cluster_entry_name: Optional[str]) -> Optional[str]:
    """IP of the kind control-plane container on the ``kind`` docker network.
    Kind names its cluster ``kind-<name>`` and node ``<name>-control-plane``.
    None when docker is absent (in-devc) or the container/network isn't there."""
    if not cluster_entry_name:
        return None
    name = cluster_entry_name.removeprefix("kind-")
    try:
        res = runner.run(
            ["docker", "inspect", "-f", "{{.NetworkSettings.Networks.kind.IPAddress}}", f"{name}-control-plane"],
            check=False,
        )
    except ToolMissing:
        return None
    ip = res.stdout.strip() if res.rc == 0 else ""
    return ip or None


def _existing_devc_servers(path: Path) -> dict[str, str]:
    """Map cluster-entry-name → server from an existing devc kubeconfig. The
    in-devc refresh (no docker) reuses this instead of re-resolving the Linux
    kind node IP the host-side refresh already wrote."""
    if not path.is_file():
        return {}
    try:
        data = _load_yaml(path)
    except WtCtlError:
        return {}
    out: dict[str, str] = {}
    for entry in data.get("clusters") or []:
        if not isinstance(entry, dict):
            continue
        inner = entry.get("cluster")
        server = inner.get("server") if isinstance(inner, dict) else None
        if isinstance(entry.get("name"), str) and isinstance(server, str):
            out[entry["name"]] = server
    return out


def _make_devc_server_resolver(runner: Runner, emit: Any, prior: dict[str, str]) -> Any:
    """Build ``resolve(entry_name, loopback_port) -> url | None`` for the
    devc-side apiserver.

    ``loopback_port`` is the host port of a ``127.0.0.1:<port>`` server, or
    None when the current server is non-loopback (e.g. a ClusterIP from the
    MC operator Secret). ``None`` return means "leave the server unchanged".

    Linux: the kind node container IP on the shared ``kind`` network at :6443,
    resolved by context name — works for both loopback and ClusterIP bases. If
    docker can't resolve it (in-devc), reuse a prior non-loopback server we
    already wrote host-side; else fall back to host.docker.internal for a
    loopback base, or leave a non-loopback one untouched.
    macOS: host.docker.internal:<port> (needs the host port, so loopback only;
    a non-loopback base is left unchanged)."""
    is_linux = platform.system() == "Linux"

    def resolve(entry_name: Optional[str], loopback_port: Optional[str]) -> Optional[str]:
        if is_linux:
            ip = _kind_node_ip(runner, entry_name)
            if ip:
                return f"https://{ip}:6443"
            prev = prior.get(entry_name or "")
            if prev and not _LOOPBACK_SERVER_RE.match(prev):
                return prev
            if loopback_port is not None:
                emit(
                    f"WARN: could not resolve kind node IP for {entry_name!r}; using host.docker.internal (may be unreachable on Linux)"
                )
                return f"https://host.docker.internal:{loopback_port}"
            emit(f"WARN: could not resolve kind node IP for non-loopback member {entry_name!r}; leaving unchanged")
            return None
        if loopback_port is not None:
            return f"https://host.docker.internal:{loopback_port}"
        return None

    return resolve


def _rewrite_member_servers_for_devc(kc: dict[str, Any], resolve: Any) -> None:
    """Rewrite each member server to the devc-reachable apiserver URL returned
    by ``resolve`` (+ TLS-skip, strip CA). Loopback servers pass their host
    port to the resolver; non-loopback servers (ClusterIP from the MC Secret)
    are rewritten by context name on Linux. A ``None`` resolution leaves the
    entry unchanged (BYOC / unsupported macOS non-loopback)."""
    for entry in kc.get("clusters") or []:
        inner = entry.get("cluster") if isinstance(entry, dict) else None
        if not isinstance(inner, dict):
            continue
        server = inner.get("server")
        m = _LOOPBACK_SERVER_RE.match(server) if isinstance(server, str) else None
        new = resolve(entry.get("name"), (m.group("port") or "443") if m else None)
        if not new:
            continue
        inner["server"] = new
        inner["insecure-skip-tls-verify"] = True
        inner.pop("certificate-authority-data", None)
        inner.pop("certificate-authority", None)


def _patch_kfp(url: str, kubeconfig_bytes: bytes, log: Any) -> bool:
    """HTTP PATCH the kubeconfig onto K8S_FWD_PROXY/kubeconfig. Returns True
    on 2xx, False otherwise. Best-effort: kfp may not be listening (host
    daemon down, in-devc proxy not yet healthy) and we don't want that to
    fail the larger refresh."""
    req = Request(
        url,
        data=kubeconfig_bytes,
        method="PATCH",
        headers={"Content-Type": "application/yaml"},
    )
    try:
        with urlopen(req, timeout=_KFP_PATCH_TIMEOUT_S) as resp:
            ok = 200 <= resp.status < 300
            if ok:
                log(f"registered kubeconfig with kfp at {url}")
            else:
                log(f"kfp rejected kubeconfig (status={resp.status}) at {url}")
            return ok
    except (HTTPError, URLError, TimeoutError, OSError) as exc:
        log(f"kfp not reachable at {url}: {exc.__class__.__name__}: {exc}; skipping registration")
        return False


class KubeconfigDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    def state_for(self, worktree_root: Path) -> Optional[KubeconfigState]:
        gen = worktree_root / ".generated"
        # Prefer the host-side file; fall back to the devc variant.
        host_kc = gen / "current.kubeconfig"
        devc_kc = gen / "current.devc.kubeconfig"
        path = host_kc if host_kc.is_file() else (devc_kc if devc_kc.is_file() else None)
        if path is None:
            return None
        last_patch = _age(path)
        # kfp registration is ephemeral and not probed from the host side;
        # "devc variant exists" is the cheapest signal that get-kubeconfig has
        # run inside the container at least once.
        kfp = devc_kc.is_file() if path else None
        return KubeconfigState(
            path=str(path),
            last_patch=last_patch,
            kfp_registered=kfp,
        )

    def refresh(
        self,
        worktree_root: Path,
        *,
        env: Optional[dict[str, str]] = None,
        in_devc: Optional[bool] = None,
        emit: Any = None,
    ) -> None:
        """Per-side post-process the host kubeconfig and register the result
        with kfp. See module docstring for the dispatch table.

        Arguments:
            worktree_root: the worktree containing ``.generated/``.
            env: env-var bag (defaults to os.environ). Lets tests inject.
            in_devc: side hint; defaults to detecting `/.dockerenv`.
            emit: logger callable (defaults to stderr writer).
        """
        if env is None:
            env = dict(os.environ)
        if in_devc is None:
            in_devc = Path("/.dockerenv").is_file()
        if emit is None:
            emit = lambda msg: sys.stderr.write(msg + "\n")

        gen = worktree_root / ".generated"
        host_kc = gen / "current.kubeconfig"
        devc_kc = gen / "current.devc.kubeconfig"
        evg_pin = gen / ".current-evg-host"

        if not host_kc.is_file():
            raise WtCtlError(
                f"{host_kc} not found. Run recreate_kind_cluster.sh (local kind), "
                "evg_host.sh get-kubeconfig (EVG host), or copy a BYOC "
                "kubeconfig into place first."
            )

        host_data = _load_yaml(host_kc)
        cluster_name = env.get("CLUSTER_NAME") or None
        k8s_fwd_proxy = env.get("K8S_FWD_PROXY") or None
        suffix_port = env.get("MCK_DEVC_PROXY_PORT") or None

        # A file inherited from a prior refresh may carry a ``-<port>`` suffix;
        # normalise back to bare so this run writes bare files.
        if suffix_port:
            _strip_kubeconfig_suffix(host_data, suffix_port)

        def _pin_current_context(kc: dict[str, Any]) -> None:
            if cluster_name:
                kc["current-context"] = cluster_name

        if evg_pin.is_file():
            # EVG-host mode: both files get proxy-url.
            if not suffix_port:
                raise WtCtlError(
                    "MCK_DEVC_PROXY_PORT must be set (`make switch` first to "
                    "source .devcontainer/.env via root-context)"
                )
            host_proxy = f"http://127.0.0.1:{suffix_port}"
            emit(f"Patching {host_kc}: proxy={host_proxy} " f"context={cluster_name or '<inherited>'} (bare on disk)")
            _set_proxy_url(host_data, host_proxy)
            _pin_current_context(host_data)
            _dump_yaml(host_kc, host_data)

            evg_host_proxy = env.get("EVG_HOST_PROXY") or None
            if evg_host_proxy:
                emit(
                    f"Patching {devc_kc}: proxy={evg_host_proxy} "
                    f"context={cluster_name or '<inherited>'} (bare on disk)"
                )
                devc_data = copy.deepcopy(host_data)
                _set_proxy_url(devc_data, evg_host_proxy)
                _dump_yaml(devc_kc, devc_data)
                self._maybe_register(
                    k8s_fwd_proxy,
                    devc_kc if in_devc else host_kc,
                    emit,
                    suffix_port=None if in_devc else suffix_port,
                )
            else:
                self._maybe_register(k8s_fwd_proxy, host_kc, emit, suffix_port=suffix_port)
            self._refresh_multicluster_evg(gen, host_proxy, evg_host_proxy, emit)
            return

        # Non-EVG branch: dispatch on the apiserver URL.
        server = _first_server(host_data)
        # Strip stale proxy-url left over from a previous EVG-host run.
        _set_proxy_url(host_data, None)
        _pin_current_context(host_data)
        _dump_yaml(host_kc, host_data)

        if server is None:
            raise WtCtlError(f"{host_kc} has no clusters[0].cluster.server")

        m = _LOOPBACK_SERVER_RE.match(server)
        if m:
            resolve = _make_devc_server_resolver(self.runner, emit, _existing_devc_servers(devc_kc))
            devc_data = copy.deepcopy(host_data)
            _rewrite_member_servers_for_devc(devc_data, resolve)
            emit(f"Local-kind: {devc_kc}: server={_first_server(devc_data)} (TLS verify off)")
            _dump_yaml(devc_kc, devc_data)
        else:
            emit(f"BYOC: {devc_kc}: identity copy of {host_kc} (server={server})")
            _dump_yaml(devc_kc, host_data)

        # Local-kind / BYOC: the host kubeconfig's apiserver is locally
        # reachable (no proxy-url). Suffix only the bytes PATCHed to the
        # on-host kfp so concurrent worktrees don't clobber each other's
        # registrations; on-disk files stay bare for the in-devc sidecar
        # and bin/reset-style tooling.
        self._maybe_register(
            k8s_fwd_proxy,
            devc_kc if in_devc else host_kc,
            emit,
            suffix_port=None if in_devc else suffix_port,
        )
        self._refresh_multicluster_local(gen, emit)

    def _refresh_multicluster_evg(
        self,
        gen: Path,
        host_proxy: str,
        devc_proxy: Optional[str],
        emit: Any,
    ) -> None:
        """EVG-host mode: derive the two multi-cluster flavors from the bare
        base. Host flavor gets ``host_proxy`` in place; devc flavor gets
        ``devc_proxy`` (only available inside the container). No-op when the
        base file hasn't been produced yet."""
        base = gen / "multicluster_kubeconfig"
        if not base.is_file():
            return
        data = _load_yaml(base)
        _set_proxy_url(data, host_proxy)
        emit(f"Patching {base}: proxy={host_proxy} (member clusters, host flavor)")
        _dump_yaml(base, data)
        if devc_proxy:
            devc_data = copy.deepcopy(data)
            _set_proxy_url(devc_data, devc_proxy)
            emit(f"Patching {gen / 'multicluster.devc.kubeconfig'}: proxy={devc_proxy} (member clusters, devc flavor)")
            _dump_yaml(gen / "multicluster.devc.kubeconfig", devc_data)

    def _refresh_multicluster_local(self, gen: Path, emit: Any) -> None:
        """local-kind / BYOC mode: host flavor is the bare base (laptop kind
        is directly reachable from host shells); devc flavor rewrites loopback
        member servers to host.docker.internal. No-op without a base file."""
        base = gen / "multicluster_kubeconfig"
        if not base.is_file():
            return
        data = _load_yaml(base)
        _set_proxy_url(data, None)
        _dump_yaml(base, data)
        devc_path = gen / "multicluster.devc.kubeconfig"
        resolve = _make_devc_server_resolver(self.runner, emit, _existing_devc_servers(devc_path))
        devc_data = copy.deepcopy(data)
        _rewrite_member_servers_for_devc(devc_data, resolve)
        emit(f"Multi-cluster: {devc_path} (devc flavor)")
        _dump_yaml(devc_path, devc_data)

    def _maybe_register(
        self,
        k8s_fwd_proxy: Optional[str],
        path: Path,
        emit: Any,
        *,
        suffix_port: Optional[str] = None,
    ) -> None:
        """PATCH the kubeconfig at ``path`` onto ``k8s_fwd_proxy``.

        When ``suffix_port`` is set, the bytes sent over the wire have
        every cluster/context/user name + current-context suffixed with
        ``-<port>`` for kfp's multi-tenant disambiguation. The file on
        disk is unchanged — every other consumer (variant context env
        vars, ``bin/reset``, in-container k8s-proxy sidecar) keeps
        using bare names.
        """
        if not k8s_fwd_proxy:
            return
        url = f"http://{k8s_fwd_proxy}/kubeconfig"
        if suffix_port:
            data = _load_yaml(path)
            _suffix_kubeconfig_names(data, suffix_port)
            yaml = _import_yaml()
            body = yaml.safe_dump(data, sort_keys=False).encode("utf-8")
            emit(f"Loading kubeconfig from {path} onto {k8s_fwd_proxy} " f"(names suffixed -{suffix_port} in-flight)")
        else:
            emit(f"Loading kubeconfig from {path} onto {k8s_fwd_proxy}")
            body = path.read_bytes()
        _patch_kfp(url, body, emit)
