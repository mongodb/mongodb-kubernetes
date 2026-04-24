"""Insert this directory onto sys.path so `from _common import ...` works.

The test files in this package import their shared helpers via the bare
module name `_common` (e.g. `from _common import FakePopenFactory`).
That works locally when pytest is invoked from inside this directory
(cwd ends up on sys.path) but not from the repo root, which is how
the Evergreen `unit_tests_python` task runs pytest.

Pytest's rootdir-walking adds `scripts/test/` (the first ancestor
without an `__init__.py`) to sys.path, which is enough to import the
`wt_ctl` package but not enough for the bare `_common` symbol. Adding
THIS directory explicitly closes the gap.
"""

from __future__ import annotations

import sys
from pathlib import Path

_THIS_DIR = str(Path(__file__).resolve().parent)
if _THIS_DIR not in sys.path:
    sys.path.insert(0, _THIS_DIR)
