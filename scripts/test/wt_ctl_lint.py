#!/usr/bin/env python3
"""Lint: ``runner.py`` is the only file in the package permitted to import
``subprocess``. Every other module receives a ``Runner`` via constructor
injection.

Exits non-zero if any forbidden import is found.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
PACKAGE = REPO / "scripts" / "dev" / "wt_ctl"
ALLOWED = {"runner.py"}

PATTERN = re.compile(r"^(import|from)\s+subprocess\b")


def main() -> int:
    if not PACKAGE.is_dir():
        print(f"[lint] package dir not found: {PACKAGE}", file=sys.stderr)
        return 2
    failures: list[tuple[Path, int, str]] = []
    for path in sorted(PACKAGE.rglob("*.py")):
        if path.name in ALLOWED:
            continue
        for lineno, line in enumerate(path.read_text().splitlines(), start=1):
            if PATTERN.search(line):
                failures.append((path, lineno, line.strip()))
    if failures:
        print("[lint] forbidden subprocess import outside runner.py:", file=sys.stderr)
        for path, lineno, line in failures:
            print(f"  {path.relative_to(REPO)}:{lineno}: {line}", file=sys.stderr)
        return 1
    print("[lint] ok: only runner.py imports subprocess.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
