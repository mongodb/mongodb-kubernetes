"""Reading the plain ``KEY=value`` files (``.devcontainer/.env``, ``.generated/context*.env``) and which side we run on."""

from __future__ import annotations

from pathlib import Path


def read_env_file(path: Path) -> dict[str, str]:
    """The unquoted values of ``path``; a missing file gives an empty dict."""
    out: dict[str, str] = {}
    if not path.is_file():
        return out
    for raw in path.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        out[key.strip()] = value
    return out


def inside_container() -> bool:
    """Whether this runs in the devcontainer: the host side has no ``/.dockerenv``."""
    return Path("/.dockerenv").exists()
