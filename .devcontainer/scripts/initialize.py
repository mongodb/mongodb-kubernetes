#!/usr/bin/env python3
"""Host-side generator for the devcontainer compose override.

Runs before any container (and any venv) exists, so it uses only the standard
library. It rebuilds services.devcontainer.{environment,volumes} from scratch
each run and writes the result to COMPOSE_OVERRIDE_FILE. JSON is valid YAML, so
`docker compose -f` accepts the output directly.
"""

import json
import os
import platform
import shutil
import subprocess
import sys


def evergreen_cli_url():
    """Where the container downloads the Linux Evergreen CLI of the same build as the host's, or None when the host
    has none. The download happens at on-create time, so the image does not change with the CLI version."""
    if shutil.which("evergreen") is None:
        print("No evergreen CLI on the host: the container gets none. Install it there by hand if you need it.")
        return None
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine(), platform.machine())
    try:
        revision = subprocess.check_output(["evergreen", "version", "--build-revision"], text=True).strip()
    except subprocess.CalledProcessError:
        print("The evergreen CLI of the host does not print its build: the container gets none.")
        return None
    return (
        f"https://evg-bucket-evergreen.s3.amazonaws.com/evergreen/clients/evergreen_{revision}/linux_{arch}/evergreen"
    )


def git_common_dir():
    """The main repo's .git as an absolute path when the workspace is a linked worktree, else None.

    The worktree's .git file names <main>/.git/worktrees/<name>, so git only works in the container
    when the main .git is mounted at the same path."""
    git_dir = subprocess.check_output(["git", "rev-parse", "--git-common-dir"], text=True).strip()
    return git_dir if git_dir.startswith("/") else None


def git_identity():
    """The host's commit identity as the variables git reads, so that a commit or a tag in the container has an
    author: the container has no ~/.gitconfig."""
    env = []
    for key, name in (("user.name", "NAME"), ("user.email", "EMAIL")):
        value = subprocess.run(["git", "config", key], capture_output=True, text=True).stdout.strip()
        if value:
            env += [f"GIT_AUTHOR_{name}={value}", f"GIT_COMMITTER_{name}={value}"]
    return env


def ssh_agent_socket():
    """Host ssh-agent socket to forward, or empty string when unavailable.

    macOS/Docker Desktop exposes a fixed forwarding socket; on Linux the host
    shell's SSH_AUTH_SOCK is used (must be set when initializeCommand runs)."""
    system = platform.system()
    if system == "Darwin":
        return "/run/host-services/ssh-auth.sock"
    if system == "Linux":
        return os.environ.get("SSH_AUTH_SOCK", "")
    return ""


def host_mounts():
    """Read-only mounts of the host files that exist: the tmux config (plugin dirs are left out, TPM plugins may carry
    platform-specific binaries) and the Evergreen config and tokens. Docker would create a missing source as a
    root-owned directory in the user's home, so only existing paths are listed. The tokens go to /host-kanopy,
    which post-start.sh copies without the refresh token."""
    home = os.environ["HOME"]
    entries = []
    for src, dst in (
        (".tmux.conf", "/home/vscode/.tmux.conf"),
        (".config/tmux", "/home/vscode/.config/tmux"),
        (".evergreen.yml", "/home/vscode/.evergreen.yml"),
        (".kanopy", "/host-kanopy"),
    ):
        if os.path.exists(os.path.join(home, src)):
            entries.append((os.path.join(home, src), dst))
    return entries


def main():
    override_file = os.environ["COMPOSE_OVERRIDE_FILE"]

    # The container sees its own Linux, not the host's OS. wt-ctl reads this to skip the macOS-only kfp daemon.
    environment = [f"MCK_DEVC_HOST_OS={platform.system()}", *git_identity()]
    volumes = []

    git_dir = git_common_dir()
    if git_dir is not None:
        volumes.append(f"{git_dir}:{git_dir}:cached")
        print(f"Added git worktree volume: {git_dir}")

    # The autossh sidecar waits for this agent, and kubectl through gost-proxy needs the sidecar.
    host_sock = ssh_agent_socket()
    if host_sock:
        volumes.append(f"{host_sock}:/ssh-auth.sock")
        environment.append("SSH_AUTH_SOCK=/ssh-auth.sock")
        print(f"Added ssh-agent volume: {host_sock} -> /ssh-auth.sock")
    else:
        print("No host ssh-agent socket found; the EVG host tunnel needs one")

    for src, dst in host_mounts():
        entry = f"{src}:{dst}:ro"
        volumes.append(entry)
        print(f"Added host volume: {entry}")

    cli_url = evergreen_cli_url()
    if cli_url is not None:
        environment.append(f"EVERGREEN_CLI_URL={cli_url}")

    devcontainer = {}
    if environment:
        devcontainer["environment"] = environment
    if volumes:
        devcontainer["volumes"] = volumes

    doc = {"services": {"devcontainer": devcontainer}}
    with open(override_file, "w") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")


if __name__ == "__main__":
    sys.exit(main())
