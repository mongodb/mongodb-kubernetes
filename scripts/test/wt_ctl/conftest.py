"""Put this directory on sys.path so `from _common import ...` resolves.

Pytest's rootdir-walking adds `scripts/test/` (the nearest ancestor without
`__init__.py`), which imports the `wt_ctl` package but not the bare
`_common` symbol when pytest runs from the repo root. This closes the gap.
"""

from __future__ import annotations

import sys
from pathlib import Path

_THIS_DIR = str(Path(__file__).resolve().parent)
if _THIS_DIR not in sys.path:
    sys.path.insert(0, _THIS_DIR)
