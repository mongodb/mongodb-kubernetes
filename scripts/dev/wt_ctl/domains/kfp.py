"""On-host kube-forwarding-proxy (kfp) daemon lifecycle.

The kfp binary path is resolved in order:

  1. ``$MCK_KFP_BINARY`` environment variable, if set.
  2. ``$PATH`` lookup for the literal name ``k8s-service-proxy``
     (the binary's installed name; matches a ``brew install``,
     ``go install``, or release-tarball setup).
  3. ``~/mdb/kube-forwarding-proxy/bin/proxy`` — the legacy
     "checked-out + locally-built" layout this repo's contributors
     have historically used.

The daemon runs on the developer laptop and exposes:

  - HTTP control on ``127.0.0.1:11616`` (``/healthz`` + kubeconfig PATCH API)
  - DNS server  on ``127.0.0.1:11617``
  - SOCKS proxy on ``127.0.0.1:11618`` (when ``-socks`` is passed)

Test-stack scripts on the EVG host (``setup_kind_cluster.sh``,
``evg_host.sh``) PATCH the freshly-minted kubeconfig back to the laptop's
kfp via a reverse SSH tunnel that the worktree's ``evg-host-proxy``
sidecar establishes. If kfp isn't running, those PATCHes silently no-op
(see Phase 2 best-effort workarounds) — we don't want a missing daemon
to fail the create pipeline.

This domain is a thin wrapper around the binary:

  - ``is_listening()`` — fast TCP check on port 11616 (stdlib socket).
  - ``health()`` — GET ``/healthz`` with stdlib urllib.
  - ``pid()`` — read the pidfile and verify the PID is alive.
  - ``start()`` — nohup the binary, write the pidfile, wait for healthz.
  - ``stop()`` — best-effort kill the recorded PID, remove the pidfile.

State files:

  - ``~/.cache/mck-devc/host-kfp/pid`` — recorded PID
  - ``~/.cache/mck-devc/host-kfp/log`` — captured stdout+stderr

Subprocess discipline: this module never imports ``subprocess``. The
spawn goes through the injected ``Runner`` (``run_detached``).
"""

from __future__ import annotations

import errno
import os
import shutil
import signal
import socket
import time
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

BINARY_ENV_VAR = "MCK_KFP_BINARY"
BINARY_PATH_NAME = "k8s-service-proxy"
LEGACY_BINARY = Path.home() / "mdb" / "kube-forwarding-proxy" / "bin" / "proxy"
DEFAULT_STATE_DIR = Path.home() / ".cache" / "mck-devc" / "host-kfp"


@dataclass(frozen=True)
class _BinaryResolution:
    """Snapshot of the chain at one resolution moment. ``chosen`` is the
    path ``resolve_binary()`` returned; the other fields are what each
    step saw, for use in the error trace.
    """
    chosen: Path
    env_value: str            # "" when $MCK_KFP_BINARY is unset/empty
    path_lookup: Optional[Path]  # None when k8s-service-proxy not on PATH

    def trace(self) -> str:
        return (
            f"resolution: ${BINARY_ENV_VAR}={self.env_value or '(unset)'}, "
            f"PATH lookup '{BINARY_PATH_NAME}'="
            f"{self.path_lookup or '(not found)'}, "
            f"legacy {LEGACY_BINARY}"
        )


def resolve_binary_with_trace(
    explicit: Optional[Path] = None,
) -> _BinaryResolution:
    """Resolve the kfp binary path and capture the trace in one pass.

    Order:
      1. ``explicit`` (caller-supplied override) — bypasses the chain.
      2. ``$MCK_KFP_BINARY`` env var.
      3. ``$PATH`` lookup for ``k8s-service-proxy`` (the binary's
         installed name from kfp's Dockerfile).
      4. The legacy ``~/mdb/kube-forwarding-proxy/bin/proxy`` checkout.

    Returning the chain alongside ``chosen`` lets ``start()``'s error
    message reflect exactly what ``resolve_binary()`` saw, even if
    ``$PATH`` / ``$MCK_KFP_BINARY`` change between calls.
    """
    env_value = os.environ.get(BINARY_ENV_VAR, "").strip()
    on_path = shutil.which(BINARY_PATH_NAME)
    path_lookup = Path(on_path) if on_path else None
    if explicit is not None:
        chosen = Path(explicit)
    elif env_value:
        chosen = Path(env_value).expanduser()
    elif path_lookup is not None:
        chosen = path_lookup
    else:
        chosen = LEGACY_BINARY
    return _BinaryResolution(
        chosen=chosen, env_value=env_value, path_lookup=path_lookup,
    )


def resolve_binary(explicit: Optional[Path] = None) -> Path:
    """Return the kfp binary path that ``start()`` will exec. See
    ``resolve_binary_with_trace`` for the resolution chain.
    """
    return resolve_binary_with_trace(explicit).chosen


def binary_resolution_trace() -> str:
    """One-line summary of the binary-resolution chain. Convenience
    wrapper for callers that only need the trace string.
    """
    return resolve_binary_with_trace().trace()

LISTEN_PROBE_TIMEOUT_S = 0.25
HEALTH_PROBE_TIMEOUT_S = 1.0
START_HEALTH_DEADLINE_S = 5.0
START_POLL_INTERVAL_S = 0.2


class KfpStartFailed(WtCtlError):
    """The kfp binary was spawned but ``/healthz`` never came up."""

    exit_code = 1


@dataclass
class KfpStatus:
    listening: bool
    pid: Optional[int]
    health: Optional[str]
    http_endpoint: str = f"{KFP_HOST}:{KFP_HTTP_PORT}"
    dns_endpoint: str = f"{KFP_HOST}:{KFP_DNS_PORT}"


class KfpDomain:
    """Thin lifecycle wrapper around the on-host kfp binary.

    The binary path is resolved at construction time via
    ``resolve_binary_with_trace`` (see module docstring for the chain),
    so a subsequent ``$PATH`` / ``$MCK_KFP_BINARY`` change doesn't make
    the error message contradict ``self.binary``.
    """

    def __init__(
        self,
        runner: Runner,
        *,
        binary: Optional[Path] = None,
        state_dir: Optional[Path] = None,
    ) -> None:
        self.runner = runner
        # Resolve binary + trace together so start()'s error message
        # reflects the exact chain that picked self.binary, even if the
        # env/PATH later changes.
        resolution = resolve_binary_with_trace(binary)
        self.binary = resolution.chosen
        self._binary_trace = resolution.trace()
        self.state_dir = state_dir or DEFAULT_STATE_DIR
        self.pid_file = self.state_dir / "pid"
        self.log_file = self.state_dir / "log"

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
            with urllib.request.urlopen(
                KFP_HEALTH_URL, timeout=HEALTH_PROBE_TIMEOUT_S
            ) as resp:
                if resp.status != 200:
                    return None
                body = resp.read().decode("utf-8", errors="replace").strip()
                if not body:
                    return None
                return "ok" if "ok" in body.lower() else body
        except (urllib.error.URLError, urllib.error.HTTPError, OSError, ValueError):
            return None

    def pid(self) -> Optional[int]:
        """Recorded PID if the pidfile exists AND the PID is alive."""
        if not self.pid_file.is_file():
            return None
        try:
            text = self.pid_file.read_text().strip()
        except OSError:
            return None
        if not text.isdigit():
            return None
        recorded = int(text)
        if not _pid_alive(recorded):
            return None
        return recorded

    def status(self) -> KfpStatus:
        return KfpStatus(
            listening=self.is_listening(),
            pid=self.pid(),
            health=self.health(),
        )

    # ------------------------------------------------------------------
    # lifecycle
    # ------------------------------------------------------------------
    def start(self) -> int:
        """Start the daemon if it isn't already listening.

        Returns the PID of the running daemon. Raises ``KfpStartFailed`` if
        the spawn happened but ``/healthz`` doesn't come up within
        ``START_HEALTH_DEADLINE_S``. If the binary is missing, raises
        ``KfpStartFailed`` (since we can't even spawn it).
        """
        # Already up? Return whatever PID we know about (may be None if the
        # daemon was started outside wt-ctl — that's fine).
        if self.is_listening():
            existing = self.pid()
            return existing if existing is not None else 0

        if not self.binary.is_file():
            raise KfpStartFailed(
                f"kfp binary not found at {self.binary}. "
                f"{self._binary_trace}. "
                f"Either install '{BINARY_PATH_NAME}' on PATH, set "
                f"${BINARY_ENV_VAR}=/abs/path/to/proxy, or build from "
                f"source into {LEGACY_BINARY}."
            )

        self.state_dir.mkdir(parents=True, exist_ok=True)
        # Truncate the log so each start gets a fresh one (useful for
        # debugging mid-pipeline failures).
        self.log_file.write_text("")

        new_pid = self.runner.run_detached(
            [str(self.binary)],
            stdout_path=self.log_file,
            stderr_path=self.log_file,
        )
        # Record the PID atomically.
        tmp = self.pid_file.with_suffix(".tmp")
        tmp.write_text(f"{new_pid}\n")
        tmp.replace(self.pid_file)

        # Wait for /healthz.
        deadline = time.monotonic() + START_HEALTH_DEADLINE_S
        while time.monotonic() < deadline:
            if self.health() is not None:
                return new_pid
            # Bail out early if the PID died (binary crashed at startup).
            if not _pid_alive(new_pid):
                tail = _tail(self.log_file, 20)
                raise KfpStartFailed(
                    f"kfp daemon (pid={new_pid}) exited before /healthz came up"
                    + (f"\n--- log tail ---\n{tail}" if tail else "")
                )
            time.sleep(START_POLL_INTERVAL_S)
        tail = _tail(self.log_file, 20)
        raise KfpStartFailed(
            f"kfp /healthz did not respond within {START_HEALTH_DEADLINE_S:g}s "
            f"(pid={new_pid}, log={self.log_file})"
            + (f"\n--- log tail ---\n{tail}" if tail else "")
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
            raise WtCtlError(
                f"kfp daemon not listening at {KFP_HOST}:{KFP_HTTP_PORT}; "
                f"start it first with `wt-ctl kfp start`"
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
            raise WtCtlError(
                f"kfp {method.lower()} failed: HTTP {exc.code} {exc.reason}"
            ) from exc
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
            raise WtCtlError(
                f"kfp daemon not listening at {KFP_HOST}:{KFP_HTTP_PORT}"
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
            raise WtCtlError(
                f"kfp delete failed: HTTP {exc.code} {exc.reason}"
            ) from exc
        except (urllib.error.URLError, OSError) as exc:
            raise WtCtlError(f"kfp delete failed: {exc}") from exc

    def stop(self) -> Optional[int]:
        """Best-effort: kill the recorded PID, remove the pidfile.

        Returns the PID we tried to kill (or ``None`` if no pidfile / dead).
        Never raises.
        """
        pid = self.pid()
        if pid is None:
            # No live PID; remove a stale pidfile if present.
            if self.pid_file.is_file():
                try:
                    self.pid_file.unlink()
                except OSError:
                    pass
            return None
        try:
            os.kill(pid, signal.SIGTERM)
        except OSError:
            pass
        # Give it a beat to exit, then remove the pidfile.
        for _ in range(10):
            if not _pid_alive(pid):
                break
            time.sleep(0.1)
        if _pid_alive(pid):
            try:
                os.kill(pid, signal.SIGKILL)
            except OSError:
                pass
        try:
            self.pid_file.unlink()
        except OSError:
            pass
        return pid


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

def _pid_alive(pid: int) -> bool:
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except OSError as exc:
        if exc.errno == errno.ESRCH:
            return False
        if exc.errno == errno.EPERM:
            # Process exists but we can't signal it — count as alive.
            return True
        return False
    return True


def _tail(path: Path, lines: int) -> str:
    if not path.is_file():
        return ""
    try:
        text = path.read_text()
    except OSError:
        return ""
    out = text.splitlines()[-lines:]
    return "\n".join(out)
