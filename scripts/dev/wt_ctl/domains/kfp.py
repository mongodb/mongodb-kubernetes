"""On-host kube-forwarding-proxy (kfp) — pkg-installed, launchd-owned.

The daemon ships as a macOS pkg (``packaging/macos/build.sh`` in the
``kube-forwarding-proxy`` repo). The pkg installs:

  - ``/usr/local/bin/k8s-service-proxy``
  - ``/Library/LaunchDaemons/io.github.fealebenpae.k8s-proxy.plist``
    (RunAtLoad + KeepAlive in the ``system`` launchd domain)
  - ``/private/etc/resolver/svc.cluster.local`` (resolver entry for the
    macOS DNS lookup chain so ``*.svc.cluster.local`` queries reach the
    daemon on ``127.0.0.1:11617``)

Endpoints (per the shipped plist):

  - HTTP control on ``127.0.0.1:11616`` (``/healthz`` + kubeconfig PATCH API)
  - DNS server  on ``127.0.0.1:11617``
  - SOCKS proxy on ``127.0.0.1:11618`` (only when started with ``-socks``;
    the shipped plist does not enable it)

The daemon is started by launchd at boot and respawned automatically
(KeepAlive=true). **wt-ctl does not start or stop it.** Lifecycle ops are
manual ``launchctl`` calls — see ``cmd_kfp`` for the exact strings.

What this domain still does:

  - ``is_listening()`` — fast TCP check on port 11616.
  - ``health()`` — GET ``/healthz``.
  - ``status()`` — combines the two for display.
  - ``register()`` — HTTP PATCH/PUT a kubeconfig onto ``/kubeconfig``.
    kfp's per-context shutdown means peer worktrees' registrations survive.
  - ``reset()``  — HTTP DELETE the daemon's dynamic kubeconfig.
"""

from __future__ import annotations

import os
import socket
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from ..errors import WtCtlError
from ..runner import Runner
from .kubeconfig import _suffix_kubeconfig_names

KFP_HOST = "127.0.0.1"
KFP_HTTP_PORT = 11616
KFP_DNS_PORT = 11617
KFP_DEFAULT_URL = f"http://{KFP_HOST}:{KFP_HTTP_PORT}"
KFP_HEALTH_URL = f"{KFP_DEFAULT_URL}/healthz"

LAUNCHD_LABEL = "io.github.fealebenpae.k8s-proxy"
LAUNCHCTL_KICKSTART = f"sudo launchctl kickstart -k system/{LAUNCHD_LABEL}"
LAUNCHCTL_BOOTOUT = f"sudo launchctl bootout system/{LAUNCHD_LABEL}"
LAUNCHCTL_BOOTSTRAP = f"sudo launchctl bootstrap system " f"/Library/LaunchDaemons/{LAUNCHD_LABEL}.plist"

LISTEN_PROBE_TIMEOUT_S = 0.25
HEALTH_PROBE_TIMEOUT_S = 1.0


def _resolve_suffix_port(kubeconfig_path: Path) -> Optional[str]:
    """Derive ``MCK_DEVC_PROXY_PORT`` for the worktree owning ``kubeconfig_path``.

    Looks up two parents (``.generated/<file>`` → worktree root), then reads
    the line out of ``.devcontainer/.env``. Falls back to the environment
    variable; returns ``None`` if neither source has it (the caller then
    skips the suffix and PATCHes bare bytes — fine for non-devc workflows).
    """
    env_port = os.environ.get("MCK_DEVC_PROXY_PORT")
    worktree = kubeconfig_path.resolve().parent.parent
    env_file = worktree / ".devcontainer" / ".env"
    if env_file.is_file():
        for line in env_file.read_text().splitlines():
            if line.startswith("MCK_DEVC_PROXY_PORT="):
                value = line.split("=", 1)[1].strip()
                if value:
                    return value
    return env_port or None


class KfpUnavailable(WtCtlError):
    """The on-host kfp daemon isn't reachable on 127.0.0.1:11616."""

    exit_code = 1


@dataclass
class KfpStatus:
    listening: bool
    health: Optional[str]
    http_endpoint: str = f"{KFP_HOST}:{KFP_HTTP_PORT}"
    dns_endpoint: str = f"{KFP_HOST}:{KFP_DNS_PORT}"


class KfpDomain:
    """Thin HTTP client for the on-host kfp daemon.

    Daemon lifecycle is owned by launchd via the macOS pkg; this domain
    only probes liveness and PATCHes kubeconfigs. ``runner`` is kept on the
    signature for symmetry with other domains (unused: status() spawns no
    subprocesses).
    """

    def __init__(
        self,
        runner: Runner,
        *,
        binary: Optional[Path] = None,  # kept for test-seam compat; unused
        state_dir: Optional[Path] = None,  # kept for test-seam compat; unused
    ) -> None:
        self.runner = runner
        # `binary` / `state_dir` retained as kwargs for test-fixture compat; no
        # runtime effect (lifecycle is launchd's job).
        self.binary = binary
        self.state_dir = state_dir

    # ------------------------------------------------------------------
    # liveness probes
    # ------------------------------------------------------------------
    def is_listening(self, host: str = KFP_HOST, port: int = KFP_HTTP_PORT) -> bool:
        """``True`` if anything binds ``host:port`` right now."""
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(LISTEN_PROBE_TIMEOUT_S)
        try:
            s.connect((host, port))
            return True
        except (OSError, socket.timeout):
            return False
        finally:
            try:
                s.close()
            except OSError:
                pass

    def health(self) -> Optional[str]:
        """Return ``'ok'`` if ``/healthz`` returns 200 with body containing
        ``ok``; otherwise ``None``. Never raises.
        """
        try:
            with urllib.request.urlopen(KFP_HEALTH_URL, timeout=HEALTH_PROBE_TIMEOUT_S) as resp:
                if resp.status != 200:
                    return None
                body = resp.read().decode("utf-8", errors="replace").strip()
                if not body:
                    return None
                return "ok" if "ok" in body.lower() else body
        except (urllib.error.URLError, urllib.error.HTTPError, OSError, ValueError):
            return None

    def status(self) -> KfpStatus:
        return KfpStatus(
            listening=self.is_listening(),
            health=self.health(),
        )

    def register(
        self,
        kubeconfig_path: Path,
        *,
        replace: bool = False,
        target_url: str = KFP_DEFAULT_URL,
        suffix_port: Optional[str] = None,
    ) -> str:
        """Send the kubeconfig file's contents to the kfp daemon at
        ``<target_url>/kubeconfig``. Defaults to the host's launchd kfp on
        ``http://127.0.0.1:11616``.

        Identity inside the daemon is ``(name, proxy-port)``. We rewrite the
        cluster/context/user names in-flight to ``<name>-<suffix_port>`` so
        peer worktrees don't collide on bare names. ``suffix_port`` falls
        back to ``$MCK_DEVC_PROXY_PORT``; on-disk files stay bare for the
        in-devc sidecar and ``bin/reset``-style tooling.

        ``replace=True`` uses PUT to wipe every other registration. Prefer
        the default PATCH, which only merges same-name entries.
        """
        if not kubeconfig_path.is_file():
            raise WtCtlError(f"kubeconfig not found: {kubeconfig_path}")

        parsed = urllib.parse.urlsplit(target_url)
        probe_host = parsed.hostname or KFP_HOST
        probe_port = parsed.port or KFP_HTTP_PORT
        if not self.is_listening(probe_host, probe_port):
            raise KfpUnavailable(
                f"kfp daemon not listening at {probe_host}:{probe_port}. "
                f"Daemon is pkg-installed and launchd-managed; "
                f"restart with: {LAUNCHCTL_KICKSTART}"
            )

        port = suffix_port or _resolve_suffix_port(kubeconfig_path)
        if port:
            try:
                import yaml  # type: ignore
            except ImportError as exc:
                raise WtCtlError(
                    "wt-ctl kfp register requires pyyaml to suffix cluster "
                    "names for the on-wire PATCH; pip install pyyaml."
                ) from exc
            data = yaml.safe_load(kubeconfig_path.read_text())
            if not isinstance(data, dict):
                raise WtCtlError(f"{kubeconfig_path} did not parse as a YAML mapping")
            _suffix_kubeconfig_names(data, port)
            body = yaml.safe_dump(data, sort_keys=False).encode("utf-8")
        else:
            body = kubeconfig_path.read_bytes()

        method = "PUT" if replace else "PATCH"
        endpoint = target_url.rstrip("/") + "/kubeconfig"
        req = urllib.request.Request(endpoint, data=body, method=method)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as exc:
            raise WtCtlError(f"kfp {method.lower()} failed: HTTP {exc.code} {exc.reason}") from exc
        except (urllib.error.URLError, OSError) as exc:
            raise WtCtlError(f"kfp {method.lower()} failed: {exc}") from exc

    def reset(self) -> None:
        """HTTP DELETE the daemon's dynamic kubeconfig — wipes all
        registered contexts. Use to clean up stale entries whose proxy-url
        ports are invalid (e.g. ``kind-kind-80640``) before re-registering
        active worktrees.

        Raises ``WtCtlError`` if the daemon isn't listening or the
        HTTP call fails.
        """
        if not self.is_listening():
            raise KfpUnavailable(
                f"kfp daemon not listening at {KFP_HOST}:{KFP_HTTP_PORT}. "
                f"Daemon is pkg-installed and launchd-managed; "
                f"restart with: {LAUNCHCTL_KICKSTART}"
            )
        req = urllib.request.Request(
            f"http://{KFP_HOST}:{KFP_HTTP_PORT}/kubeconfig",
            method="DELETE",
        )
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                # 204 No Content on success
                _ = resp.read()
        except urllib.error.HTTPError as exc:
            raise WtCtlError(f"kfp delete failed: HTTP {exc.code} {exc.reason}") from exc
        except (urllib.error.URLError, OSError) as exc:
            raise WtCtlError(f"kfp delete failed: {exc}") from exc
