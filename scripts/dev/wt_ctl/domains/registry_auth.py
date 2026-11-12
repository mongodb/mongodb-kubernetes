"""Registry credentials of a worktree: the ECR tokens it pulls images with, kept in one store.

An ECR token lives 12 hours and carries its own expiry, so the store is the only record. It is
``<worktree>/.generated/registry-auth/config.json``: the kind nodes mount its directory, the devcontainer gets a copy
as ``~/.docker/config.json``, and the pull secrets in the clusters are rebuilt from that copy. The user's
``~/.docker/config.json`` is read as one source of tokens and never written. Tokens stay out of argv and messages.
"""

from __future__ import annotations

import base64
import binascii
import json
import os
import sys
import tempfile
import time
from pathlib import Path
from typing import Optional

from ..errors import ExternalCommandFailed, WtCtlError
from ..runner import Runner
from .devcontainer import DevcontainerDomain
from .evg import pinned_host_name, push_registry_auth, read_registry_auth

ECR_REGISTRIES = {
    "268558157000.dkr.ecr.us-east-1.amazonaws.com": "us-east-1",
    "268558157000.dkr.ecr.eu-west-1.amazonaws.com": "eu-west-1",
}
RENEW_BELOW_S = 4 * 3600
CONTAINER_COPY = "/home/vscode/.docker/config.json"
COPY_TO_CONTAINER = (
    f"install -d -o vscode -g vscode /home/vscode/.docker && cat > {CONTAINER_COPY} && "
    f"chown vscode:vscode {CONTAINER_COPY} && chmod 600 {CONTAINER_COPY}"
)
MINT_FIX = "aws sso login --profile <name>, or set the AWS keys in scripts/dev/contexts/private-context"


def _padded(value: str) -> str:
    return value + "=" * (-len(value) % 4)


def decode_ecr_expiry(auth: str) -> Optional[float]:
    """Expiry (epoch seconds) of an ECR ``auth`` value, ``base64("AWS:" + base64(json))``; None for anything else."""
    try:
        user, _, password = base64.b64decode(_padded(auth.strip()), validate=True).decode().partition(":")
        if user != "AWS":
            return None
        return float(json.loads(base64.b64decode(_padded(password), validate=True))["expiration"])
    except (binascii.Error, ValueError, KeyError, TypeError, AttributeError):
        return None


def parse_auths(text: str) -> dict:
    try:
        auths = json.loads(text).get("auths")
    except (ValueError, AttributeError):
        return {}
    return auths if isinstance(auths, dict) else {}


def read_auths(path: Path) -> dict:
    try:
        return parse_auths(path.read_text())
    except OSError:
        return {}


def store_path(wt: Path) -> Path:
    return wt / ".generated" / "registry-auth" / "config.json"


def store_text(auths: dict) -> str:
    return json.dumps({"auths": auths}, indent=2) + "\n"


def write_store(wt: Path, auths: dict) -> None:
    """Replace the store in one step, so a node that reads it never sees half a file. Directory 700, file 600."""
    path = store_path(wt)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.parent.chmod(0o700)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".config.json.")  # mkstemp creates the file with mode 600
    with open(fd, "w") as f:
        f.write(store_text(auths))
    os.replace(tmp, path)


def _expiry(auths: dict, registry: str) -> Optional[float]:
    return decode_ecr_expiry((auths.get(registry) or {}).get("auth") or "")


def seconds_left(auths: dict) -> Optional[float]:
    """Time left on the token that ends first; None when an ECR registry has no readable token."""
    expiries = [_expiry(auths, registry) for registry in ECR_REGISTRIES]
    return None if None in expiries else min(expiries) - time.time()


def describe(auths: Optional[dict]) -> tuple[int, str]:
    """How a copy of the credentials looks, as (exit class, text): 0 fine, 1 expiring, 3 expired or missing."""
    left = seconds_left(auths or {})
    if left is None:
        return 3, "missing"
    if left <= 0:
        return 3, "expired"
    text = f"{int(left // 3600)}h {int(left % 3600 // 60)}m left"
    return (1, f"expiring, {text}") if left < RENEW_BELOW_S else (0, f"ok, {text}")


def mint(runner: Runner) -> dict:
    """A new token for every ECR region from ``aws ecr get-login-password``."""
    if not runner.have("aws"):
        raise WtCtlError("the aws CLI is not installed: install AWS CLI v2")
    # private-context sets the keys to "undefined" for SSO users, and aws would send those instead of the profile.
    unset = [
        a
        for k in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY")
        if os.environ.get(k) in ("", "undefined")
        for a in ("-u", k)
    ]
    fresh = {}
    for registry, region in ECR_REGISTRIES.items():
        argv = ["env", *unset, "aws", "ecr", "get-login-password", "--region", region]
        argv += ["--cli-connect-timeout", "10", "--cli-read-timeout", "20"]
        try:
            password = runner.run(argv, env={"AWS_PAGER": ""}, input_text="").stdout.strip()
        except ExternalCommandFailed as exc:
            reason = (exc.stderr.strip().splitlines() or ["aws failed without a message"])[-1]
            raise WtCtlError(f"could not mint an ECR token: {reason}\n  fix: {MINT_FIX}") from exc
        fresh[registry] = {"auth": base64.b64encode(f"AWS:{password}".encode()).decode()}
    return fresh


def ensure_store(runner: Runner, wt: Path, *, force: bool = False) -> bool:
    """Make the store hold ECR tokens with time left: the better of the store's and the user's docker config's,
    and a new one when that has under 4 hours. Returns whether a token was minted."""
    stored = read_auths(store_path(wt))
    user = read_auths(Path.home() / ".docker" / "config.json")
    auths = {}
    for registry in ECR_REGISTRIES:
        entries = [a[registry] for a in (stored, user) if (a.get(registry) or {}).get("auth")]
        if entries:
            auths[registry] = max(entries, key=lambda e: decode_ecr_expiry(e["auth"]) or 0)
    # The kind nodes used to read the user's file, so its inline credentials for other registries stay available.
    auths.update({r: e for r, e in user.items() if r not in ECR_REGISTRIES and e.get("auth")})
    left = seconds_left(auths)
    minted = False
    if force or left is None or left < RENEW_BELOW_S:
        try:
            auths.update(mint(runner))
            minted = True
        except WtCtlError as exc:
            if force or left is None or left <= 0:
                raise
            sys.stderr.write(f"[wt-ctl] warning: {exc.render()}\n")
    if auths != stored:
        write_store(wt, auths)
    return minted


def copy_to_container(runner: Runner, devc: DevcontainerDomain, wt: Path) -> bool:
    """Write the store as the devcontainer's docker config; False when the container is not running."""
    cid = devc.container_id()
    if cid:
        runner.run(
            ["docker", "exec", "-i", "-u", "root", cid, "bash", "-c", COPY_TO_CONTAINER],
            input_text=store_text(read_auths(store_path(wt))),
        )
    return bool(cid)


def read_container_copy(runner: Runner, devc: DevcontainerDomain) -> Optional[dict]:
    """The devcontainer's credentials, None when it is not running."""
    cid = devc.container_id()
    if not cid:
        return None
    return parse_auths(runner.run(["docker", "exec", cid, "cat", CONTAINER_COPY], check=False).stdout)


def read_host_copy(runner: Runner, wt: Path) -> Optional[dict]:
    """The credentials on the Evergreen host, which its kind nodes mount; None when the host does not answer."""
    try:
        return parse_auths(read_registry_auth(runner, wt))
    except WtCtlError:
        return None


def push_to_host(runner: Runner, wt: Path) -> None:
    push_registry_auth(runner, wt, store_text(read_auths(store_path(wt))))


def roll(runner: Runner, wt: Path, log: Path) -> bool:
    """Copy the store to the Evergreen host of the worktree, if it has one, and into the devcontainer, and rebuild the
    clusters' pull secrets from it; False when the container is down. The secrets script skips namespaces that do
    not exist yet."""
    if pinned_host_name(wt):
        try:
            push_to_host(runner, wt)
        except WtCtlError as exc:
            sys.stderr.write(f"[wt-ctl] warning: the Evergreen host did not get the credentials: {exc.render()}\n")
    devc = DevcontainerDomain(runner, wt)
    if not copy_to_container(runner, devc, wt):
        return False
    devc.bash(". scripts/funcs/kubernetes; create_image_registries_secret", log)
    return True
