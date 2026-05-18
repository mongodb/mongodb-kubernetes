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
(KeepAlive=true). **wt-ctl does not start or stop it.** Lifecycle ops
that used to live here are now manual ``launchctl`` calls — see
``cmd_kfp`` for the exact strings.

What this domain still does:

  - ``is_listening()`` — fast TCP check on port 11616.
  - ``health()`` — GET ``/healthz``.
  - ``status()`` — combines the two for display.
  - ``register()`` — HTTP PATCH/PUT a kubeconfig onto ``/kubeconfig``.
    Unaffected by the lifecycle change; kfp's per-context shutdown
    (de51427) means peer worktrees' registrations survive.
  - ``reset()``  — HTTP DELETE the daemon's dynamic kubeconfig.
"""

from __future__ import annotations

import socket
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from ..errors import WtCtlError
from ..runner import Runner

KFP_HOST = "127.0.0.1"
KFP_HTTP_PORT = 11616
KFP_DNS_PORT = 11617
KFP_HEALTH_URL = f"http://{KFP_HOST}:{KFP_HTTP_PORT}/healthz"

LAUNCHD_LABEL = "io.github.fealebenpae.k8s-proxy"
LAUNCHCTL_KICKSTART = f"sudo launchctl kickstart -k system/{LAUNCHD_LABEL}"
LAUNCHCTL_BOOTOUT = f"sudo launchctl bootout system/{LAUNCHD_LABEL}"
LAUNCHCTL_BOOTSTRAP = (
    f"sudo launchctl bootstrap system "
    f"/Library/LaunchDaemons/{LAUNCHD_LABEL}.plist"
)

LISTEN_PROBE_TIMEOUT_S = 0.25
HEALTH_PROBE_TIMEOUT_S = 1.0


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
    only probes liveness and PATCHes kubeconfigs. ``runner`` is kept on
    the signature for symmetry with other domains (status() emits no
    subprocesses today, so it goes unused).
    """

    def __init__(
        self,
        runner: Runner,
        *,
        binary: Optional[Path] = None,  # kept for test-seam compat; unused
        state_dir: Optional[Path] = None,  # kept for test-seam compat; unused
    ) -> None:
        self.runner = runner
        # `binary` / `state_dir` retained as kwargs so test fixtures that
        # construct KfpDomain with these named args don't break. They have
        # no runtime effect now that lifecycle is launchd's job.
        self.binary = binary
        self.state_dir = state_dir

    # ------------------------------------------------------------------
    # liveness probes
    # ------------------------------------------------------------------
    def is_listening(self) -> bool:
        """``True`` if anything binds 127.0.0.1:11616 right now."""
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(LISTEN_PROBE_TIMEOUT_S)
        try:
            s.connect((KFP_HOST, KFP_HTTP_PORT))
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

    def register(self, kubeconfig_path: Path, *, replace: bool = False) -> str:
        """Send the kubeconfig file's contents to the on-host kfp daemon
        at ``http://127.0.0.1:11616/kubeconfig``. Mirrors what
        ``setup_kind_cluster.sh`` and ``evg_host.sh`` curl on the EVG
        host's side.

        ``replace=False`` (default): HTTP PATCH — merge into the existing
        dynamic config, overwriting same-name entries (the registrant's
        worktree-suffixed context names ensure peer worktrees don't
        collide on names). With the daemon's per-context shutdown the
        re-registration is idempotent AND non-disruptive for peer
        worktrees' in-flight listeners. **This is what you want
        99% of the time.**

        ``replace=True``: HTTP PUT — wipe the entire dynamic config and
        replace it with just this kubeconfig. **This blows away every
        peer worktree's registration**; the daemon's per-context
        shutdown will correctly tear down those peer listeners and
        release their VIPs, but peers will have to re-register before
        their host-side tooling works again. Reserve for the rare
        "I want a clean slate" case where ``kfp reset && register`` is
        what you'd otherwise type.

        Returns the response body. Raises ``WtCtlError`` if the file is
        missing, the daemon isn't listening, or the HTTP call fails.
        """
        if not kubeconfig_path.is_file():
            raise WtCtlError(f"kubeconfig not found: {kubeconfig_path}")
        if not self.is_listening():
            raise KfpUnavailable(
                f"kfp daemon not listening at {KFP_HOST}:{KFP_HTTP_PORT}. "
                f"Daemon is pkg-installed and launchd-managed; "
                f"restart with: {LAUNCHCTL_KICKSTART}"
            )
        body = kubeconfig_path.read_bytes()
        method = "PUT" if replace else "PATCH"
        req = urllib.request.Request(
            f"http://{KFP_HOST}:{KFP_HTTP_PORT}/kubeconfig",
            data=body,
            method=method,
        )
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as exc:
            raise WtCtlError(f"kfp {method.lower()} failed: HTTP {exc.code} {exc.reason}") from exc
        except (urllib.error.URLError, OSError) as exc:
            raise WtCtlError(f"kfp {method.lower()} failed: {exc}") from exc

    def reset(self) -> None:
        """HTTP DELETE the daemon's dynamic kubeconfig — wipes all
        registered contexts. Use to clean up stale entries from prior
        runs whose proxy-url ports were invalid (e.g. legacy
        ``kind-kind-80640``) before re-registering active worktrees.

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

