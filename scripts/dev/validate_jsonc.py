#!/usr/bin/env python3
"""Validate JSON-with-comments files (JSONC).

Used by the pre-commit `check-jsonc` hook for devcontainer.json and other
JSONC sources where the strict json schema check-json hook cannot apply.

Strips // line comments, /* block */ comments, and trailing commas before
parsing with the standard json module. Exits non-zero with the file path
and the json error message if any file fails.
"""

from __future__ import annotations

import json
import re
import sys

_TRAILING_COMMA = re.compile(r",(\s*[\]}])")


def strip_jsonc(raw: str) -> str:
    """Strip JSONC comments (// line and /* block */) and trailing commas.

    String literals are preserved untouched so URL substrings like
    ``https://example`` aren't mis-parsed as comments.
    """
    out: list[str] = []
    i = 0
    n = len(raw)
    in_str = False
    while i < n:
        ch = raw[i]
        if in_str:
            out.append(ch)
            if ch == "\\" and i + 1 < n:
                out.append(raw[i + 1])
                i += 2
                continue
            if ch == '"':
                in_str = False
            i += 1
            continue
        if ch == '"':
            in_str = True
            out.append(ch)
            i += 1
            continue
        if ch == "/" and i + 1 < n:
            nxt = raw[i + 1]
            if nxt == "/":
                end = raw.find("\n", i + 2)
                i = n if end == -1 else end
                continue
            if nxt == "*":
                end = raw.find("*/", i + 2)
                if end == -1:
                    raise ValueError("unterminated /* block comment")
                i = end + 2
                continue
        out.append(ch)
        i += 1
    return _TRAILING_COMMA.sub(r"\1", "".join(out))


def main(argv: list[str]) -> int:
    rc = 0
    for path in argv[1:]:
        try:
            with open(path, encoding="utf-8") as fp:
                raw = fp.read()
            json.loads(strip_jsonc(raw))
        except (json.JSONDecodeError, ValueError) as exc:
            print(f"{path}: {exc}", file=sys.stderr)
            rc = 1
        except OSError as exc:
            print(f"{path}: {exc}", file=sys.stderr)
            rc = 1
    return rc


if __name__ == "__main__":
    sys.exit(main(sys.argv))
