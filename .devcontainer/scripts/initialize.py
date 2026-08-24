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
    """Resolve the Linux Evergreen CLI URL anchored on the host's evergreen
    build revision. The download happens at on-create time so the image stays
    stable across version bumps. Returns None when evergreen is absent."""
    if shutil.which("evergreen") is None:
        print("Evergreen CLI not found on host; skipping URL resolution. The on-create")
        print("step will be a no-op and you'll need to install evergreen manually inside")
        print("the container.")
        return None

    arch = platform.machine()
    if arch == "x86_64":
        arch = "amd64"
    elif arch == "aarch64":
        arch = "arm64"

    revision = subprocess.check_output(["evergreen", "version", "--build-revision"], text=True).strip()
    return (
        "https://evg-bucket-evergreen.s3.amazonaws.com/evergreen/clients/"
        f"evergreen_{revision}/linux_{arch}/evergreen"
    )


def git_common_dir():
    """Absolute git common dir when the workspace is a worktree, else None."""
    git_dir = subprocess.check_output(["git", "rev-parse", "--git-common-dir"], text=True).strip()
    return git_dir if git_dir.startswith("/") else None


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


def tmux_volumes():
    """Read-only mounts for host tmux config paths that exist. Plugin dirs are
    skipped: TPM plugins may carry platform-specific binaries."""
    home = os.environ["HOME"]
    entries = []
    for src, dst in (
        (os.path.join(home, ".tmux.conf"), "/home/vscode/.tmux.conf"),
        (os.path.join(home, ".config", "tmux"), "/home/vscode/.config/tmux"),
    ):
        if os.path.exists(src):
            entries.append((src, dst))
    return entries


def render_config(tooling_root, workspace_root, devc_dir):
    """Render the per-worktree devcontainer config next to the compose
    override:

    * ``compose.yml`` — verbatim copy of the tooling's template (all host
      paths in it are ``${MCK_WORKSPACE_DIR}`` / ``${MCK_TOOLING_DIR}``
      variables resolved from the sibling ``.env``).
    * ``devcontainer.json`` — tooling template with the compose-file pointers
      rewritten to plain sibling names and initializeCommand to the tooling's
      absolute path (lifecycle cwd = workspace folder, which may not carry
      the tooling).
    * ``.env`` — upsert MCK_WORKSPACE_DIR / MCK_TOOLING_DIR, preserving any
      MCK_DEVC_NET_* lines wt-ctl's net_allocate appended; migrate the
      MCK_DEVC_* block from a legacy ``.devcontainer/.env`` once.
    """
    src_devc = os.path.join(tooling_root, ".devcontainer")

    shutil.copyfile(os.path.join(src_devc, "compose.yml"), os.path.join(devc_dir, "compose.yml"))

    json_text = open(os.path.join(src_devc, "devcontainer.json")).read()
    replacements = [
        ('"../.generated/devcontainer/compose.yml"', '"compose.yml"'),
        ('"../.generated/devcontainer/compose.generated.yml"', '"compose.generated.yml"'),
        ('"../.generated/devcontainer/compose.user.yml"', '"compose.user.yml"'),
        (
            '"initializeCommand": "bash .devcontainer/scripts/initialize.sh"',
            f'"initializeCommand": "bash {src_devc}/scripts/initialize.sh"',
        ),
    ]
    for old, new in replacements:
        if json_text.count(old) != 1:
            raise SystemExit(f"devcontainer.json template drifted: expected exactly one {old!r}")
        json_text = json_text.replace(old, new)
    with open(os.path.join(devc_dir, "devcontainer.json"), "w") as f:
        f.write(json_text)

    env_path = os.path.join(devc_dir, ".env")
    lines = []
    if os.path.exists(env_path):
        lines = [ln.rstrip("\n") for ln in open(env_path)]
    managed = {
        "MCK_WORKSPACE_DIR": workspace_root,
        "MCK_TOOLING_DIR": tooling_root,
    }
    kept = [ln for ln in lines if ln.split("=", 1)[0] not in managed]
    if not any(ln.startswith("MCK_DEVC_NET_PREFIX=") for ln in kept):
        legacy = os.path.join(workspace_root, ".devcontainer", ".env")
        if os.path.exists(legacy):
            migrated = [
                ln.rstrip("\n")
                for ln in open(legacy)
                if ln.startswith("MCK_DEVC_") and ln.split("=", 1)[0] not in managed
            ]
            if migrated:
                kept += migrated
                print(f"migrated MCK_DEVC_* block from legacy {legacy}")
    rendered = [f"{k}={v}" for k, v in managed.items()] + kept
    with open(env_path, "w") as f:
        f.write("\n".join(ln for ln in rendered if ln.strip()) + "\n")
    print(f"rendered devcontainer config in {devc_dir}")


def main():
    override_file = os.environ["COMPOSE_OVERRIDE_FILE"]

    render_config(
        os.environ["TOOLING_ROOT"],
        os.environ["WORKSPACE_ROOT"],
        os.environ["DEVC_DIR"],
    )

    environment = []
    volumes = []

    cli_url = evergreen_cli_url()
    if cli_url is not None:
        environment.append(f"EVERGREEN_CLI_URL={cli_url}")
        print(f"Set EVERGREEN_CLI_URL={cli_url} on devcontainer service in " f"{os.path.basename(override_file)}")

    git_dir = git_common_dir()
    if git_dir is not None:
        volumes.append(f"{git_dir}:{git_dir}:cached")
        print(f"Added git worktree volume: {git_dir}")

    # autossh in the sidecar blocks forever without this forward, breaking
    # kubectl through gost-proxy.
    host_sock = ssh_agent_socket()
    if host_sock:
        volumes.append(f"{host_sock}:/ssh-auth.sock")
        environment.append("SSH_AUTH_SOCK=/ssh-auth.sock")
        print(f"ssh-agent.sh: mounted {host_sock} -> /ssh-auth.sock and exported SSH_AUTH_SOCK")
    else:
        print("ssh-agent.sh: no host ssh-agent socket detected; skipping")

    for src, dst in tmux_volumes():
        entry = f"{src}:{dst}:ro"
        volumes.append(entry)
        print(f"Added tmux config volume: {entry}")

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
