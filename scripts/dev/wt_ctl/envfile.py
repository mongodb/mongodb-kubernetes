"""Shared parser for the generated ``.env``-style files.

``.generated/context.env``, ``.generated/context.<side>.env`` and the
rendered devcontainer ``.env`` are all plain ``KEY=value`` lines with
optional surrounding shell quotes.
"""

from __future__ import annotations

from pathlib import Path


def read_env_file(path: Path) -> dict[str, str]:
    """Parse ``path`` into a dict of unquoted values.

    Missing file → empty dict. Comments, blank lines and lines without
    ``=`` are ignored.
    """
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
