"""Entry point: ``python3 -m wt_ctl ...`` dispatches into ``cli.main``."""

from __future__ import annotations

import sys

from .cli import main

if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
