"""Parser test for ``dc_select_network.sh --list`` output."""

from __future__ import annotations

import unittest

from _common import FakePopenFactory  # noqa: E402
from wt_ctl.domains.network import _parse_list_output  # noqa: E402

SAMPLE = """\
Registry: /home/x/.cache/mck-devc/net-prefix-registry
Worktree parent (conventional): /home/x/mdb

BRANCH_DIR                                               PREFIX  STATUS  WORKTREE
----------                                               ------  ------  --------
alpha_devcontainer                                       28      active  /home/x/mdb/alpha_devcontainer
beta                                                     30      stale   (missing)

Summary: 1 active, 1 stale (run with --prune to GC).

Docker networks in 172.[16-31].0.0/16:
NETWORK                                                            PREFIX
-------                                                            ------
alpha_devcontainer_devcontainer_devcontainer                       28

Free range: 172.[16-31].0.0/16.
"""


class NetworkParseTests(unittest.TestCase):
    def test_parses_active_and_stale_rows(self) -> None:
        rows = _parse_list_output(SAMPLE)
        self.assertEqual(len(rows), 2)
        self.assertEqual(rows[0].branch_dir, "alpha_devcontainer")
        self.assertEqual(rows[0].prefix, 28)
        self.assertEqual(rows[0].status, "active")
        self.assertEqual(rows[0].worktree_path, "/home/x/mdb/alpha_devcontainer")
        self.assertEqual(rows[1].branch_dir, "beta")
        self.assertEqual(rows[1].status, "stale")
        self.assertIsNone(rows[1].worktree_path)


if __name__ == "__main__":
    unittest.main()
