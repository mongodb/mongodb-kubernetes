"""Evergreen hosts: the host a worktree's kind clusters run on, and the ssh path from the worktree to it.

Hosts come from the ``evergreen`` CLI, whose JSON is the contract. ``scripts/dev/evg_host.sh`` does the rest: the
sync, the clusters and the kubeconfig, in a directory of the worktree's own (``/home/ubuntu/devc<N>/``), so several
worktrees share one host. A host that ``create`` spawned carries the marker file ``~/.wt-ctl-host``, and ``delete``
terminates only such a host, when its last worktree leaves.

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
from pathlib import Path
from typing import Callable, Optional

from ..errors import ExternalCommandFailed, WtCtlError
from ..runner import Runner
from .devcontainer import DevcontainerDomain
from .network import flock

DEFAULT_DISTRO = "ubuntu2204-latest-large"
DEFAULT_REGION = "eu-west-1"
DEAD = ("terminated", "decommissioned", "quarantined", "failed")
MARKER = "~/.wt-ctl-host"
EVERGREEN_TIMEOUT_S = 30
POLL_S = 5
READ_ATTEMPTS = 3
READ_RETRY_S = 10

# Runs on the host through ssh: this worktree's clusters and directory go, and the output tells what remains.
LEAVE_SCRIPT = (
    "kind=/home/ubuntu/devc{n}/mongodb-kubernetes/bin/kind; "
    "if [ -x $kind ]; then for c in $($kind get clusters | grep '^devc{n}-'); do $kind delete cluster --name $c; done; fi; "
    "rm -rf /home/ubuntu/devc{n}; echo REMAINING=$(ls -d /home/ubuntu/devc*/ 2>/dev/null | wc -l); "
    "if [ -f " + MARKER + " ]; then echo CREATED=1; fi"
)
# In place: the kind nodes mount the file. umask does not narrow a file that exists (evg_host.sh made it 664), so chmod.
PUSH_SCRIPT = "umask 077; mkdir -p ~/.docker; cat > ~/.docker/config.json; chmod 600 ~/.docker/config.json"


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


def running_host(hosts: list[dict], name: str) -> Optional[dict]:
    """The host ``name`` once it runs and has an address, else None."""
    host = pick_host(hosts, name)
    return host if host and host.get("status") == "running" and host.get("host_name") else None


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

    def evergreen(self, *args: str) -> str:
        if not self.runner.have("evergreen"):
            raise WtCtlError("evergreen not found on PATH: https://spruce.mongodb.com/preferences/cli")
        # Empty stdin, so that a login prompt of the CLI ends at once instead of waiting for a terminal.
        return self.runner.run(["evergreen", *args], input_text="", timeout=EVERGREEN_TIMEOUT_S).stdout

    def read(self, *args: str) -> str:
        """A command that changes nothing, retried: calls of parallel worktrees fight over the CLI's token file."""
        for _ in range(READ_ATTEMPTS - 1):
            try:
                return self.evergreen(*args)
            except ExternalCommandFailed:
                time.sleep(READ_RETRY_S)
        return self.evergreen(*args)

    def hosts(self) -> list[dict]:
        return parse_hosts(self.read("host", "list", "--mine", "--json"))

    def find(self, name: str) -> Optional[dict]:
        return pick_host(self.hosts(), name)

    def terminate(self, host_id: str) -> None:
        self.evergreen("host", "terminate", "--host", host_id)

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

    def spawn(self, name: str, distro: str, region: str, lock: Path) -> None:
        """Create a host and give it the display name ``name``. A new host has a placeholder id until AWS
        gives it a real one, so under a lock (two creates would see each other's hosts as new) it is found by what
        was not in the list before. A host that cannot be named is terminated: nobody could find it again."""
        key = self.key_name()
        new = None
        with flock(lock):
            known = {h.get("id") for h in self.hosts()}
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
            except (KeyboardInterrupt, WtCtlError):
                if new is not None:
                    try:
                        self.terminate(new["id"])
                    except WtCtlError:
                        sys.stderr.write(f"[wt-ctl] could not terminate {new['id']}: evergreen host terminate --host\n")
                raise

    def prepare_host(self, wt: Path, name: str, distro: str, region: str, lock: Path) -> None:
        """Find or spawn the host, record its address for the worktree and check that its ssh path to it works."""
        created = self.find(name) is None
        if created:
            sys.stderr.write(f"[wt-ctl] no host named {name}: spawning one\n")
            self.spawn(name, distro, region, lock)
        host = self.wait_for(f"host {name} to run", lambda hosts: running_host(hosts, name), 600)
        address = host["host_name"]
        generated = wt / ".generated"
        (generated / "ssh").mkdir(parents=True, exist_ok=True)
        (generated / ".current-evg-host-address").write_text(address)
        self.authorize(wt, address, "touch " + MARKER if created else "true")

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
        match = re.search(r"REMAINING=(\d+)", out)
        if match is None or int(match.group(1)) > 0:
            sys.stderr.write(f"[wt-ctl] host {name} keeps running for its other worktrees\n")
        elif "CREATED=1" not in out:
            sys.stderr.write(f"[wt-ctl] host {name} was not spawned by wt-ctl: left running\n")
        else:
            sys.stderr.write(f"[wt-ctl] terminating host {name}, its last worktree is gone\n")
            self.terminate(host["id"])
