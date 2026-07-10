"""Run the no-subprocess-in-domains lint as a unit test."""

from __future__ import annotations

import os
import subprocess
import sys
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
LINT = REPO / "scripts" / "test" / "wt_ctl_lint.py"


class LintTests(unittest.TestCase):
    def test_no_subprocess_in_domains(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(LINT)],
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr or proc.stdout)


if __name__ == "__main__":
    unittest.main()
