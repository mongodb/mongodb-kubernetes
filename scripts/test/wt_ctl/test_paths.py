"""Cwd resolution: inside / outside a worktree."""

from __future__ import annotations

import os
import tempfile
import unittest
from pathlib import Path

from _common import FakePopenFactory, fake_which  # noqa: E402
from wt_ctl.errors import NotInWorktree  # noqa: E402
from wt_ctl.paths import resolve_worktree  # noqa: E402
from wt_ctl.runner import Runner  # noqa: E402


class PathsTests(unittest.TestCase):
    def test_outside_worktree_raises(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            r = Runner(popen_factory=FakePopenFactory({}), which=fake_which)
            with self.assertRaises(NotInWorktree):
                resolve_worktree(r, cwd=Path(tmp))

    def test_inside_worktree_resolves(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            # macOS routes /var/folders -> /private/var/folders; resolve up
            # front so the FakePopenFactory keys match the real argv.
            wt = (Path(tmp) / "wt").resolve()
            wt.mkdir()
            (wt / ".git").write_text("gitdir: /fake\n")
            wt_str = str(wt)
            factory = FakePopenFactory(
                {
                    ("git", "-C", wt_str, "rev-parse", "--show-toplevel"): (wt_str + "\n", "", 0),
                    ("git", "-C", wt_str, "rev-parse", "--git-common-dir"): (
                        str(Path(tmp).resolve() / "main" / ".git") + "\n",
                        "",
                        0,
                    ),
                    ("git", "-C", wt_str, "rev-parse", "--abbrev-ref", "HEAD"): ("topic/x\n", "", 0),
                }
            )
            r = Runner(popen_factory=factory, which=fake_which)
            refs = resolve_worktree(r, cwd=wt)
            self.assertEqual(refs.worktree_root, wt)
            self.assertEqual(refs.branch, "topic/x")
            self.assertEqual(refs.branch_dir, "wt")
            self.assertFalse(refs.is_main)


if __name__ == "__main__":
    unittest.main()
