"""Kubeconfig domain — read summary, post-process per-side variants, register
with kfp.

`state_for` (and the legacy ``_age`` helper) are used by ``wt-ctl status``.

`refresh` is the work that ``refresh_kubeconfig.sh`` used to do in bash —
folded here so we get pyyaml-based parsing, stdlib urllib for the kfp PATCH,
and unit-testability. Dispatch:

   .generated/.current-evg-host present?
       → EVG-host mode. Host file gets proxy-url=http://127.0.0.1:<PROXY_PORT>;
         devc variant gets proxy-url=${EVG_HOST_PROXY}.
   else, host kubeconfig's server is a loopback URL?
       → local-kind. Rewrite devc variant's server to
         https://host.docker.internal:<port>, set insecure-skip-tls-verify,
         strip CA.
   else
       → BYOC. Identity-copy host kubeconfig to devc variant; assume the
         apiserver is reachable from inside the devcontainer via standard
         resolution.

Both modes also pin `current-context` to ${CLUSTER_NAME} (when set in env)
and PATCH the appropriate per-side variant to ${K8S_FWD_PROXY}/kubeconfig.

Name disambiguation across worktrees (host kfp is shared via the macOS pkg
daemon): when MCK_DEVC_PROXY_PORT is set, every cluster/context/user name in
the **host** kubeconfig gets suffixed with ``-<MCK_DEVC_PROXY_PORT>`` and
the suffixed CLUSTER_NAME is written to ``.generated/cluster_name`` so
host-side tooling (root-context) can pin kubectl to the suffixed context.
Identity becomes ``(name, proxy-port)``: re-PATCHing the same worktree
overwrites in place (idempotent across ``kind delete && kind create``),
while peer worktrees with different ports keep their own entries — kfp's
per-context shutdown (de51427) only touches the affected names. Without
MCK_DEVC_PROXY_PORT (e.g. EVG-CI runs that don't have the devc stack) we
skip the rewrite.

The **devc** kubeconfig stays bare. The in-container k8s-proxy sidecar is
single-tenant per worktree so name collisions are impossible there, and
variant context files (``e2e_static_multi_cluster_kind`` and friends)
hardcode bare cluster names for ``CLUSTER_NAME`` / ``MEMBER_CLUSTERS`` /
``TEST_POD_CLUSTER`` / ``CENTRAL_CLUSTER`` — suffixing devc names would
break them with no compensating benefit.
"""

from __future__ import annotations

import copy
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from ..errors import WtCtlError
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
    path.write_text(yaml.safe_dump(data, sort_keys=False))


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

    Identity becomes ``(name, port)`` — the same disambiguation the
    server-side rewrite in kfp's unmerged ``0d10eab`` would have done.
    Skips entries whose name already ends with the suffix so re-runs of
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


def _set_server_for_devc(kc: dict[str, Any], server: str) -> None:
    """Rewrite the apiserver URL + flag insecure-skip-tls-verify for the
    devc-side kubeconfig (local-kind cert covers 127.0.0.1 only)."""
    for entry in kc.get("clusters") or []:
        inner = entry.get("cluster") if isinstance(entry, dict) else None
        if not isinstance(inner, dict):
            continue
        inner["server"] = server
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
        # kfp registration is ephemeral; we don't probe it from the host side
        # in Phase 1 — flagging "yes" only when the devc variant exists is
        # the cheapest signal that get-kubeconfig has been run inside the
        # container at least once.
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
        cluster_name_file = gen / "cluster_name"

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

        # On a re-refresh against an already-suffixed on-disk host kc,
        # normalise back to bare names first so the devc-side deep-copy
        # snapshot stays bare. _apply_suffix re-suffixes the host copy
        # at the end. Names + current-context.
        if suffix_port:
            _strip_kubeconfig_suffix(host_data, suffix_port)

        # CLUSTER_NAME may also already carry the `-<port>` suffix:
        # root-context sources .generated/cluster_name (the suffixed
        # name) and re-exports it into context.env. Strip it so
        # _pin_current_context sets the bare form; _apply_suffix re-adds
        # the suffix to the host copy at the end.
        if cluster_name and suffix_port:
            bare_suffix = f"-{suffix_port}"
            if cluster_name.endswith(bare_suffix):
                cluster_name = cluster_name[: -len(bare_suffix)]

        def _pin_current_context(kc: dict[str, Any]) -> None:
            if cluster_name:
                kc["current-context"] = cluster_name

        def _apply_suffix(kc: dict[str, Any]) -> None:
            if suffix_port:
                _suffix_kubeconfig_names(kc, suffix_port)

        def _write_cluster_name_file() -> None:
            # Mirror whatever current-context the host kc ended up with, so
            # root-context / downstream tooling sees the suffixed CLUSTER_NAME.
            # If neither suffix nor CLUSTER_NAME pin applied, the file
            # tracks the kubeconfig's current-context verbatim — same as
            # root-context's pre-existing "yq current-context" fallback.
            cur = host_data.get("current-context")
            if isinstance(cur, str) and cur:
                cluster_name_file.write_text(f"{cur}\n")
            elif cluster_name_file.is_file():
                cluster_name_file.unlink()

        if evg_pin.is_file():
            # EVG-host mode: both files get proxy-url.
            if not suffix_port:
                raise WtCtlError(
                    "MCK_DEVC_PROXY_PORT must be set (`make switch` first to "
                    "source .devcontainer/.env via root-context)"
                )
            host_proxy = f"http://127.0.0.1:{suffix_port}"
            emit(
                f"Patching {host_kc}: proxy={host_proxy} "
                f"context={cluster_name or '<inherited>'} suffix=-{suffix_port}"
            )
            _set_proxy_url(host_data, host_proxy)
            _pin_current_context(host_data)
            # devc variant snapshot BEFORE suffixing — keeps in-container
            # consumers and variant context env vars working with bare names.
            devc_data = copy.deepcopy(host_data)
            _apply_suffix(host_data)
            _dump_yaml(host_kc, host_data)
            _write_cluster_name_file()

            evg_host_proxy = env.get("EVG_HOST_PROXY") or None
            if evg_host_proxy:
                emit(
                    f"Patching {devc_kc}: proxy={evg_host_proxy} "
                    f"context={cluster_name or '<inherited>'} (bare names)"
                )
                _set_proxy_url(devc_data, evg_host_proxy)
                _dump_yaml(devc_kc, devc_data)
                self._maybe_register(k8s_fwd_proxy, devc_kc if in_devc else host_kc, emit)
            else:
                self._maybe_register(k8s_fwd_proxy, host_kc, emit)
            return

        # Non-EVG branch: dispatch on the apiserver URL.
        server = _first_server(host_data)
        # Strip stale proxy-url left over from a previous EVG-host run.
        _set_proxy_url(host_data, None)
        _pin_current_context(host_data)

        if server is None:
            raise WtCtlError(f"{host_kc} has no clusters[0].cluster.server")

        # devc variant snapshot from the bare host_data, before suffix.
        m = _LOOPBACK_SERVER_RE.match(server)
        if m:
            port = m.group("port") or "443"
            devc_server = f"https://host.docker.internal:{port}"
            emit(
                f"Local-kind: {devc_kc}: server={devc_server} (TLS verify off)"
            )
            devc_data = copy.deepcopy(host_data)
            _set_server_for_devc(devc_data, devc_server)
            _dump_yaml(devc_kc, devc_data)
        else:
            emit(
                f"BYOC: {devc_kc}: identity copy of {host_kc} (server={server})"
            )
            # Write the bare host_data to the devc file rather than copying
            # the on-disk host file later — host_data is about to be
            # suffixed, and the devc variant must stay bare.
            _dump_yaml(devc_kc, host_data)

        # Now suffix + dump the host kubeconfig for kfp ingestion.
        _apply_suffix(host_data)
        _dump_yaml(host_kc, host_data)
        _write_cluster_name_file()

        self._maybe_register(k8s_fwd_proxy, devc_kc if in_devc else host_kc, emit)

    def _maybe_register(self, k8s_fwd_proxy: Optional[str], path: Path, emit: Any) -> None:
        if not k8s_fwd_proxy:
            return
        url = f"http://{k8s_fwd_proxy}/kubeconfig"
        emit(f"Loading kubeconfig from {path} onto {k8s_fwd_proxy}")
        body = path.read_bytes()
        _patch_kfp(url, body, emit)
