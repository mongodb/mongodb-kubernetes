"""Evergreen hosts: the host a worktree's kind clusters run on, and the ssh path from the worktree to it.

Hosts come from the ``evergreen`` CLI, whose JSON is the contract. ``scripts/dev/evg_host.sh`` does the rest: the
sync, the clusters and the kubeconfig, in a directory of the worktree's own (``/home/ubuntu/devc<N>/``), so several
worktrees share one host. A host that ``create`` spawned carries the instance tag ``wt-ctl`` (``spawn`` sets it in the
call that names the host), and ``delete`` terminates only such a host, when its last worktree leaves. The tag is read
through Evergreen's GraphQL API, which the host list does not show it in. When that API does not answer, the old marker
file ``~/.wt-ctl-host`` of hosts that an earlier wt-ctl spawned decides, with a warning.

A worktree with a host has two pin files in ``.generated/``: ``.current-evg-host`` (the display name) and
``.current-evg-host-address`` (the ssh address, which the ``evg-host-proxy`` sidecar reads). Its host key is in
``.generated/ssh/known_hosts``, which the sidecar checks strictly.
"""

from __future__ import annotations

import json
import os
import re
import sys
import time
import urllib.request
from pathlib import Path
from typing import Callable, Optional

from ..errors import ExternalCommandFailed, WtCtlError
from ..runner import Runner
from .devcontainer import DevcontainerDomain
from .network import flock

DEFAULT_DISTRO = "ubuntu2204-latest-large"
DEFAULT_REGION = "eu-west-1"
DEFAULT_HOST_HOURS = 24
DEAD = ("terminated", "decommissioned", "quarantined", "failed")
GRAPHQL_URL = "https://evergreen.corp.mongodb.com/graphql/query"
TAG_KEY = "wt-ctl"  # the instance tag that ``spawn`` sets, with the host's name as its value
LEGACY_MARKER = "~/.wt-ctl-host"  # what an earlier wt-ctl wrote on the hosts it spawned
EVERGREEN_TIMEOUT_S = 30
TERMINATE_TIMEOUT_S = 120  # a terminate has been seen to outlast the 30 s of the other calls
POLL_S = 5
READ_ATTEMPTS = 3
READ_RETRY_S = 10

# Runs on the host through ssh: this worktree's clusters and directory go, and the output tells what remains.
# LEGACY=1 says the host has the old marker file, for when the tag cannot be read.
LEAVE_SCRIPT = (
    "kind=/home/ubuntu/devc{n}/mongodb-kubernetes/bin/kind; "
    "if [ -x $kind ]; then for c in $($kind get clusters | grep '^devc{n}-'); do $kind delete cluster --name $c; done; fi; "
    "rm -rf /home/ubuntu/devc{n}; echo REMAINING=$(ls -d /home/ubuntu/devc*/ 2>/dev/null | wc -l); "
    "if [ -f " + LEGACY_MARKER + " ]; then echo LEGACY=1; fi"
)
# In place: the kind nodes mount the file. umask does not narrow a file that exists (evg_host.sh made it 664), so chmod.
PUSH_SCRIPT = "umask 077; mkdir -p ~/.docker; cat > ~/.docker/config.json; chmod 600 ~/.docker/config.json"


class GraphqlFailed(WtCtlError):
    """Evergreen's GraphQL API did not answer, or answered without the host."""


def parse_hosts(stdout: str) -> list[dict]:
    """The JSON array of ``evergreen host list --json``. The CLI may print a status line before it."""
    start = stdout.find("[")
    try:
        hosts = json.loads(stdout[start:]) if start >= 0 else None
    except ValueError:
        hosts = None
    if not isinstance(hosts, list):
        raise WtCtlError("`evergreen host list --json` did not print a JSON list")
    return hosts


def pick_host(hosts: list[dict], name: str) -> Optional[dict]:
    """The host with display name ``name`` that is not terminated or failed, if there is one."""
    for host in hosts:
        if host.get("name") == name and (host.get("status") or "").lower() not in DEAD:
            return host
    return None


def host_state(hosts: Optional[list[dict]], name: str) -> str:
    """The host's status, ``GONE`` when no live host has the name, ``unknown`` when the list could not be read."""
    if hosts is None:
        return "unknown"
    host = pick_host(hosts, name)
    return "GONE" if host is None else host.get("status") or "unknown"


def running_host(hosts: list[dict], name: str) -> Optional[dict]:
    """The host ``name`` once it runs and has an address, else None."""
    host = pick_host(hosts, name)
    return host if host and host.get("status") == "running" and host.get("host_name") else None


def host_field(token: str, host_id: str, field: str) -> object:
    """One field of the host (``status``, or ``instanceTags { key value }``, whose value is a list of ``{"key": ...,
    "value": ...}`` dicts) from Evergreen's GraphQL API. The one place that makes the HTTP call."""
    query = "{ host(hostId: %s) { %s } }" % (json.dumps(host_id), field)
    headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}
    request = urllib.request.Request(GRAPHQL_URL, json.dumps({"query": query}).encode(), headers)
    try:
        with urllib.request.urlopen(request, timeout=EVERGREEN_TIMEOUT_S) as response:
            answer = json.load(response)
    except (OSError, ValueError) as exc:  # URLError, HTTPError and timeouts are OSErrors
        raise GraphqlFailed(f"Evergreen GraphQL API: {exc}") from exc
    host = (answer.get("data") or {}).get("host") if isinstance(answer, dict) else None
    value = host.get(field.split()[0]) if isinstance(host, dict) else None
    if value is None:
        raise GraphqlFailed(f"Evergreen GraphQL API has no {field.split()[0]} for host {host_id}")
    return value


def pinned_host_name(wt: Path) -> str:
    pin = wt / ".generated" / ".current-evg-host"
    return pin.read_text().strip() if pin.is_file() else ""


def host_address(wt: Path) -> str:
    address = wt / ".generated" / ".current-evg-host-address"
    if not address.is_file() or not address.read_text().strip():
        raise WtCtlError(f"{address} not found: run `wt-ctl create <branch>` to look the Evergreen host up")
    return address.read_text().strip()


def host_env(wt: Path) -> dict[str, str]:
    """What ``evg_host.sh`` needs to act for this worktree: its checkout and its own kubeconfig."""
    return {"PROJECT_DIR": str(wt), "KUBECONFIG": str(wt / ".generated" / "current.kubeconfig")}


def ssh_argv(wt: Path, address: str, command: str, *, agent_only: bool = False) -> list[str]:
    """ssh to the host with the worktree's host keys. ``agent_only`` ignores ~/.ssh, like the sidecar: the container
    has no ~/.ssh, so a key that only the config knows would pass here and fail there."""
    base = ["ssh", "-F", "/dev/null", "-o", "IdentityFile=/dev/null"] if agent_only else ["ssh"]
    known = wt / ".generated" / "ssh" / "known_hosts"
    options = ["BatchMode=yes", "ConnectTimeout=15", f"UserKnownHostsFile={known}", "StrictHostKeyChecking=yes"]
    return [*base, *(a for o in options for a in ("-o", o)), f"ubuntu@{address}", command]


def push_registry_auth(runner: Runner, wt: Path, text: str) -> None:
    """Replace the host's ``~/.docker/config.json``, which the kind nodes of every worktree on it mount."""
    runner.run(ssh_argv(wt, host_address(wt), PUSH_SCRIPT), input_text=text, timeout=60)


def read_registry_auth(runner: Runner, wt: Path) -> str:
    return runner.run(ssh_argv(wt, host_address(wt), "cat ~/.docker/config.json"), timeout=30).stdout


class EvgDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    def evergreen(self, *args: str, timeout: float = EVERGREEN_TIMEOUT_S) -> str:
        if not self.runner.have("evergreen"):
            raise WtCtlError("evergreen not found on PATH: https://spruce.mongodb.com/preferences/cli")
        # Empty stdin, so that a login prompt of the CLI ends at once instead of waiting for a terminal.
        return self.runner.run(["evergreen", *args], input_text="", timeout=timeout).stdout

    def read(self, *args: str) -> str:
        """A command that changes nothing, retried: calls of parallel worktrees fight over the CLI's token file."""
        return self.retried(lambda: self.evergreen(*args))

    @staticmethod
    def retried(call: Callable[[], object], errors: tuple = (ExternalCommandFailed,)):
        for _ in range(READ_ATTEMPTS - 1):
            try:
                return call()
            except errors:
                time.sleep(READ_RETRY_S)
        return call()

    def host_field(self, host_id: str, field: str) -> object:
        """A field of the host from the GraphQL API, with the retries of a read. Raises ``WtCtlError`` when the API
        cannot be asked."""

        def ask() -> object:
            return host_field(self.evergreen("client", "get-oauth-token").strip(), host_id, field)

        return self.retried(ask, (ExternalCommandFailed, GraphqlFailed))

    def spawned_by_wtctl(self, host_id: str) -> bool:
        """Whether the host carries the ``wt-ctl`` tag that ``spawn`` sets. Raises ``WtCtlError`` when the GraphQL
        API cannot be asked."""
        tags = self.host_field(host_id, "instanceTags { key value }")
        return any(isinstance(t, dict) and t.get("key") == TAG_KEY for t in tags)

    def hosts(self, retry: bool = True) -> list[dict]:
        return parse_hosts((self.read if retry else self.evergreen)("host", "list", "--mine", "--json"))

    def find(self, name: str) -> Optional[dict]:
        return pick_host(self.hosts(), name)

    def terminate(self, host_id: str) -> None:
        """Terminate the host, with the time a terminate needs and one more try: a call that timed out may still
        have been accepted, and calls of parallel worktrees fight over the CLI's token file."""
        argv = ("host", "terminate", "--host", host_id)
        try:
            self.evergreen(*argv, timeout=TERMINATE_TIMEOUT_S)
        except ExternalCommandFailed:
            time.sleep(POLL_S)
            self.evergreen(*argv, timeout=TERMINATE_TIMEOUT_S)

    def wait_for(self, what: str, find: Callable[[list[dict]], Optional[dict]], timeout_s: float) -> dict:
        deadline = time.monotonic() + timeout_s
        while True:
            found = find(self.hosts())
            if found is not None:
                return found
            if time.monotonic() > deadline:
                raise WtCtlError(f"timed out after {timeout_s:g}s waiting for {what}")
            time.sleep(POLL_S)

    def key_name(self) -> str:
        """The name of the public key to put on the host: ``MCK_DEVC_EVG_KEY_NAME``, else ``evg-host``, else the
        first key ``evergreen keys list`` shows."""
        if os.environ.get("MCK_DEVC_EVG_KEY_NAME"):
            return os.environ["MCK_DEVC_EVG_KEY_NAME"]
        names = re.findall(r"^\s*Name:\s*'([^']+)'", self.read("keys", "list"), re.MULTILINE)
        if not names:
            raise WtCtlError("no public key for the host: add one in Spruce, or set MCK_DEVC_EVG_KEY_NAME")
        return "evg-host" if "evg-host" in names else names[0]

    def spawn(self, name: str, distro: str, region: str, hours: int, lock: Path) -> Optional[str]:
        """Create a host, name it ``name`` and extend it to live ``hours`` hours. A new host has a placeholder id
        until AWS gives it a real one, so under a lock (two creates would see each other's hosts as new) it is found
        by what was not in the list before. A host that cannot be named or extended is terminated: nobody could
        find it again. Returns the id of the new host, or None, and spawns nothing, when a live host of that name exists
        once the lock is held: a create that held it before has spawned it."""
        key = self.key_name()
        new = None
        with flock(lock):
            hosts = self.hosts()
            if pick_host(hosts, name) is not None:
                return None
            known = {h.get("id") for h in hosts}
            try:
                self.evergreen("host", "create", "--distro", distro, "--key", key, "--region", region)
                new = self.wait_for(
                    "the new host",
                    lambda hs: next(
                        (h for h in hs if h.get("id") not in known and re.fullmatch(r"i-[0-9a-f]+", h.get("id") or "")),
                        None,
                    ),
                    240,
                )
                self.evergreen("host", "modify", "--host", new["id"], "--name", name, "--tag", f"wt-ctl={name}")
                if hours > DEFAULT_HOST_HOURS:
                    self.evergreen("host", "modify", "--host", new["id"], "--extend", str(hours - DEFAULT_HOST_HOURS))
                sys.stderr.write(f"[wt-ctl] host {name} expires in about {hours} hours\n")
                return new["id"]
            except (KeyboardInterrupt, WtCtlError):
                if new is not None:
                    try:
                        self.terminate(new["id"])
                    except WtCtlError:
                        sys.stderr.write(f"[wt-ctl] could not terminate {new['id']}: evergreen host terminate --host\n")
                raise

    def prepare_host(self, wt: Path, name: str, distro: str, region: str, hours: int, lock: Path) -> None:
        """Find or spawn the host, record its address for the worktree and check that its ssh path to it works."""
        spawned = None
        if self.find(name) is None:
            sys.stderr.write(f"[wt-ctl] no host named {name}: spawning one\n")
            spawned = self.spawn(name, distro, region, hours, lock)
        status = "unknown"

        def running(hosts: list[dict]) -> Optional[dict]:
            nonlocal status
            status = host_state(hosts, name)
            return running_host(hosts, name)

        try:
            host = self.wait_for(f"host {name} to run", running, 600)
        except WtCtlError as exc:
            raise self.stuck_host_error(name, spawned, status, exc) from exc
        address = host["host_name"]
        generated = wt / ".generated"
        (generated / "ssh").mkdir(parents=True, exist_ok=True)
        (generated / ".current-evg-host-address").write_text(address)
        self.authorize(wt, address, "true")

    def stuck_host_error(self, name: str, spawned: Optional[str], status: str, error: WtCtlError) -> WtCtlError:
        """The error for a host that did not run in time: ``error`` with the host's last known ``status``. The host
        that this create spawned (``spawned`` is its id) is terminated by that id, whatever the last read said or a
        replacement of the name, because a create that fails here leaves no worktree whose ``delete`` would end it.
        A host that existed before is not ours to end."""
        text = f"{error} (last known status of host {name}: {status})"
        if spawned is None:
            return WtCtlError(f"{text}; the host existed before this create and is left running")
        try:
            self.terminate(spawned)
        except WtCtlError:
            return WtCtlError(
                f"{text}; this create spawned it and could not terminate it: evergreen host terminate --host {spawned}"
            )
        return WtCtlError(f"{text}; this create spawned it and terminated it")

    def authorize(self, wt: Path, address: str, command: str) -> None:
        """Record the host's key in the worktree's known_hosts (trusted on first use, as ``accept-new`` would) and run
        ``command`` there through the ssh agent alone. A fresh host takes a moment to accept connections."""
        known = wt / ".generated" / "ssh" / "known_hosts"
        # evg_host.sh runs plain ssh, which reads ~/.ssh/known_hosts and prompts for an unknown host. Without a
        # terminal the prompt fails ("Host key verification failed", hidden by -q), so the key goes there as well.
        users = Path.home() / ".ssh" / "known_hosts"
        last = ""
        for _ in range(12):
            missing = [
                f
                for f in (known, users)
                if self.runner.run(["ssh-keygen", "-F", address, "-f", str(f)], check=False).rc != 0
            ]
            if missing:
                scan = self.runner.run(["ssh-keyscan", "-T", "10", address], check=False).stdout
                if not scan:
                    time.sleep(POLL_S)
                    continue
                for f in missing:
                    f.parent.mkdir(mode=0o700, exist_ok=True)
                    with f.open("a") as out:
                        out.write(scan)
            res = self.runner.run(ssh_argv(wt, address, command, agent_only=True), check=False, timeout=60)
            if res.rc == 0:
                return
            last = (res.stderr.strip().splitlines() or ["no output"])[-1]
            time.sleep(POLL_S)
        raise WtCtlError(
            f"cannot ssh to {address} with the keys of the ssh agent ({last}). The devcontainer reaches the host "
            "through the agent only: `ssh-add` the key that Evergreen put on the host (for example ~/.ssh/evg-host), "
            "then run `wt-ctl create` again."
        )

    def restart_stale_tunnel(self, wt: Path, devc: DevcontainerDomain) -> None:
        """The sidecar reads the host address once, when it starts. A new host has another address, and an old
        sidecar keeps tunnelling to the previous one (or idles, when the worktree started on local kind)."""
        cid = devc.container_id("evg-host-proxy")
        if cid and host_address(wt) not in self.runner.run(["docker", "top", cid]).stdout:
            sys.stderr.write("[wt-ctl] evg-host-proxy tunnels to another host: restarting it\n")
            self.runner.run(["docker", "restart", cid])

    def check_tunnel(self, devc: DevcontainerDomain) -> None:
        """The ``evg-host-proxy`` sidecar can ssh to the host. Without a key in the agent it loops on
        ``Permission denied`` and the first sign of it is a kubectl timeout minutes later."""
        cid = devc.container_id("evg-host-proxy")
        if not cid:
            raise WtCtlError("the evg-host-proxy container is not running: the devcontainer cannot reach the host")
        probe = 'ssh -o BatchMode=yes -o ConnectTimeout=10 ubuntu@"$(cat /generated/.current-evg-host-address)" true'
        for _ in range(15):
            res = self.runner.run(["docker", "exec", cid, "bash", "-c", probe], check=False)
            if res.rc == 0:
                return
            time.sleep(2)
        last = (res.stderr.strip().splitlines() or ["no output"])[-1]
        raise WtCtlError(f"the devcontainer cannot ssh to the Evergreen host ({last}); `docker logs {cid}` has more")

    def owns(self, host: dict, leave_output: str) -> bool:
        """Whether wt-ctl spawned the host: by its tag, or, when the tag cannot be read, by the old marker file that
        ``LEAVE_SCRIPT`` reported (``LEGACY=1``). Raises when neither tells: the host may be wt-ctl's, so ``delete``
        must be run again."""
        try:
            return self.spawned_by_wtctl(host["id"])
        except WtCtlError as exc:
            if "LEGACY=1" not in leave_output:
                raise WtCtlError(
                    f"cannot tell whether wt-ctl spawned host {host.get('name')} ({exc}) and it has no "
                    f"{LEGACY_MARKER} file: run delete again"
                ) from exc
            sys.stderr.write(
                f"[wt-ctl] warning: cannot read the {TAG_KEY} tag of host {host.get('name')} ({exc}): "
                f"going by the {LEGACY_MARKER} file on the host\n"
            )
            return True

    def leave(self, wt: Path, index: int) -> None:
        """Take a worktree off its host: its clusters and directory go. The last worktree of a host that ``create``
        spawned terminates it. Any other host keeps running. A host that does not answer is left as it is."""
        name = pinned_host_name(wt)
        if not name:
            return
        host = self.find(name)
        if host is None:
            sys.stderr.write(f"[wt-ctl] no live Evergreen host named {name}: nothing to remove there\n")
            return
        try:
            out = self.runner.run(ssh_argv(wt, host["host_name"], LEAVE_SCRIPT.format(n=index)), timeout=900).stdout
        except ExternalCommandFailed as exc:
            sys.stderr.write(f"[wt-ctl] warning: host {name} left running, its worktree not removed: {exc.render()}\n")
            return
        remaining = re.search(r"REMAINING=(\d+)", out)
        if remaining is None or int(remaining.group(1)) > 0:
            sys.stderr.write(f"[wt-ctl] host {name} keeps running for its other worktrees\n")
        elif not self.owns(host, out):
            sys.stderr.write(f"[wt-ctl] host {name} was not spawned by wt-ctl: left running\n")
        else:
            sys.stderr.write(f"[wt-ctl] terminating host {name}, its last worktree is gone\n")
            self.end_host(name, host["id"])

    def end_host(self, name: str, host_id: str) -> None:
        """Terminate the host. Two final leaves can both get here (each removes its directory before it counts the
        others), so a terminate that fails is not an error when Evergreen reports the host terminated. The host list
        cannot tell: it omits quarantined and decommissioned hosts, whose instances can still run."""
        try:
            self.terminate(host_id)
        except WtCtlError as exc:
            try:
                ended = str(self.host_field(host_id, "status")).lower() == "terminated"
            except WtCtlError:
                ended = False
            if not ended:
                raise WtCtlError(
                    f"could not terminate host {name} ({exc}): evergreen host terminate --host {host_id}"
                ) from exc
            sys.stderr.write(f"[wt-ctl] host {name} is terminated by the delete of another worktree\n")
