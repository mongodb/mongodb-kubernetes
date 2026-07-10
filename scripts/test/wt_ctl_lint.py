#!/usr/bin/env python3
"""Lint: domain modules MUST NOT import ``subprocess``.

Per ``docs/dev/wt-ctl-plan.md``, ``runner.py`` is the only file in the
package permitted to import subprocess. Domain modules receive a Runner
via constructor injection.

Exits non-zero if any forbidden import is found.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
DOMAINS = REPO / "scripts" / "dev" / "wt_ctl" / "domains"

PATTERN = re.compile(r"^(import|from)\s+subprocess\b")


def main() -> int:
    if not DOMAINS.is_dir():
        print(f"[lint] domains dir not found: {DOMAINS}", file=sys.stderr)
        return 2
    failures: list[tuple[Path, int, str]] = []
    for path in sorted(DOMAINS.glob("*.py")):
        for lineno, line in enumerate(path.read_text().splitlines(), start=1):
            if PATTERN.search(line):
                failures.append((path, lineno, line.strip()))
    if failures:
        print("[lint] forbidden subprocess import in domain modules:", file=sys.stderr)
        for path, lineno, line in failures:
            print(f"  {path.relative_to(REPO)}:{lineno}: {line}", file=sys.stderr)
        return 1
    print("[lint] ok: no domain module imports subprocess.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
