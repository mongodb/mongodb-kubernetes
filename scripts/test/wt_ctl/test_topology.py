"""Unit tests for wt-ctl topology resolution.

wt-ctl owns the single/multi decision: explicit flags beat an explicit
context, which beats the persisted ``.current_topology`` marker, which beats
the default (multi). A mismatch against the checked-out context schedules a
``switch_context.sh`` run so the generated files are rebuilt to match.
"""

from __future__ import annotations

import unittest
from pathlib import Path

from _common import FakePopenFactory  # noqa: E402  (path side-effect only)
from wt_ctl.orchestrator import CreateInputs, resolve_topology  # noqa: E402


class ResolveTopologyTests(unittest.TestCase):
    def test_explicit_multi_on_single_context_switches_to_default_multi(self) -> None:
        self.assertEqual(
            resolve_topology(explicit=True, context_explicit=None, context_is_multi=False, marker_multi=None),
            (True, "e2e_multi_cluster_kind"),
        )

    def test_explicit_single_on_multi_context_switches_to_default_single(self) -> None:
        self.assertEqual(
            resolve_topology(explicit=False, context_explicit=None, context_is_multi=True, marker_multi=None),
            (False, "root-context"),
        )

    def test_explicit_matching_context_needs_no_switch(self) -> None:
        self.assertEqual(
            resolve_topology(explicit=True, context_explicit=None, context_is_multi=True, marker_multi=None),
            (True, None),
        )
        self.assertEqual(
            resolve_topology(explicit=False, context_explicit=None, context_is_multi=False, marker_multi=None),
            (False, None),
        )

    def test_explicit_context_decides_without_flag(self) -> None:
        self.assertEqual(
            resolve_topology(
                explicit=None,
                context_explicit="e2e_multi_cluster_2_clusters",
                context_is_multi=True,
                marker_multi=None,
            ),
            (True, None),
        )
        self.assertEqual(
            resolve_topology(
                explicit=None,
                context_explicit="e2e_mdb_kind_ubi_cloudqa",
                context_is_multi=False,
                marker_multi=None,
            ),
            (False, None),
        )

    def test_explicit_flag_contradicting_explicit_context_raises(self) -> None:
        with self.assertRaises(ValueError):
            resolve_topology(
                explicit=True,
                context_explicit="e2e_mdb_kind_ubi_cloudqa",
                context_is_multi=False,
                marker_multi=None,
            )

    def test_marker_decides_when_no_flag_no_context(self) -> None:
        self.assertEqual(
            resolve_topology(explicit=None, context_explicit=None, context_is_multi=True, marker_multi=True),
            (True, None),
        )
        self.assertEqual(
            resolve_topology(explicit=None, context_explicit=None, context_is_multi=False, marker_multi=True),
            (True, "e2e_multi_cluster_kind"),
        )
        self.assertEqual(
            resolve_topology(explicit=None, context_explicit=None, context_is_multi=True, marker_multi=False),
            (False, "root-context"),
        )

    def test_default_is_multi_and_switches_single_context(self) -> None:
        self.assertEqual(
            resolve_topology(explicit=None, context_explicit=None, context_is_multi=False, marker_multi=None),
            (True, "e2e_multi_cluster_kind"),
        )
        self.assertEqual(
            resolve_topology(explicit=None, context_explicit=None, context_is_multi=True, marker_multi=None),
            (True, None),
        )


class PersistedFlagPrecedenceTests(unittest.TestCase):
    def _inputs(self, **kw):
        base = dict(
            branch="b",
            branch_dir="b",
            worktree_path=Path("/tmp/wt-topology"),
            main_repo_root=Path("/tmp/repo-topology"),
            host_worktree_root=Path("/tmp/repo-topology"),
        )
        base.update(kw)
        return CreateInputs(**base)

    def test_explicit_context_wins_over_saved(self) -> None:
        got = self._inputs(context="ctx-new").with_persisted_flags({"context": "ctx-old"})
        self.assertEqual(got.context, "ctx-new")

    def test_explicit_topology_wins_over_saved(self) -> None:
        got = self._inputs(multi_cluster=False).with_persisted_flags({"multi_cluster": True})
        self.assertFalse(got.multi_cluster)

    def test_omitted_flags_fall_back_to_saved(self) -> None:
        got = self._inputs().with_persisted_flags({"context": "ctx-old", "multi_cluster": True})
        self.assertEqual(got.context, "ctx-old")
        self.assertTrue(got.multi_cluster)


if __name__ == "__main__":
    unittest.main()
