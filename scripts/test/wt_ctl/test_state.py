"""Orchestrator state file (.generated/wt-ctl/state.json): round-trip
read/write, hash-mismatch detection, and ``clear_from`` (used by
--restart-from).
"""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from _common import FakePopenFactory  # noqa: E402  (path side-effect only)
from wt_ctl import orchestrator_state as ostate  # noqa: E402
from wt_ctl.errors import StateConflict  # noqa: E402


class StateRoundTripTests(unittest.TestCase):
    def test_initial_state_has_all_phases_pending(self) -> None:
        st = ostate.OrchestratorState.initial(
            branch="topic/x",
            inputs={"branch": "topic/x"},
        )
        self.assertEqual(st.branch, "topic/x")
        self.assertEqual(set(st.phases.keys()), set(ostate.PHASE_ORDER))
        for rec in st.phases.values():
            self.assertEqual(rec.status, ostate.PENDING)

    def test_save_then_load_round_trip(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            wt = Path(tmp) / "wt"
            wt.mkdir()
            st = ostate.OrchestratorState.initial(
                branch="lsierant/wtctl-phase2",
                inputs={"context": "e2e_smoke"},
            )
            st.set_status(
                "worktree_init",
                ostate.OK,
                input_hash="aaa",
                log="logs/setup_worktree/x.log",
            )
            ostate.save(wt, st)
            loaded = ostate.load(wt)
            self.assertIsNotNone(loaded)
            assert loaded is not None  # for type-checker
            self.assertEqual(loaded.branch, "lsierant/wtctl-phase2")
            self.assertEqual(loaded.phases["worktree_init"].status, ostate.OK)
            self.assertEqual(loaded.phases["worktree_init"].input_hash, "aaa")
            self.assertEqual(loaded.phases["dc_up"].status, ostate.PENDING)

    def test_load_returns_none_when_no_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(ostate.load(Path(tmp) / "wt"))

    def test_load_raises_on_invalid_json(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            wt = Path(tmp) / "wt"
            (wt / ".generated" / "wt-ctl").mkdir(parents=True)
            (wt / ".generated" / "wt-ctl" / "state.json").write_text("{not-json")
            with self.assertRaises(StateConflict):
                ostate.load(wt)

    def test_save_writes_atomic_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            wt = Path(tmp) / "wt"
            st = ostate.OrchestratorState.initial(branch="b", inputs={})
            ostate.save(wt, st)
            # Should be exactly state.json + nothing else (no leaked tempfile).
            files = sorted(p.name for p in (wt / ".generated" / "wt-ctl").iterdir())
            self.assertEqual(files, ["state.json"])


class HashMismatchTests(unittest.TestCase):
    def test_hash_inputs_is_deterministic(self) -> None:
        a = ostate.hash_inputs({"k": 1, "j": 2})
        b = ostate.hash_inputs({"j": 2, "k": 1})  # key order shouldn't matter
        self.assertEqual(a, b)
        c = ostate.hash_inputs({"k": 2, "j": 2})
        self.assertNotEqual(a, c)


class RestartFromTests(unittest.TestCase):
    def test_clear_from_resets_phase_and_later(self) -> None:
        st = ostate.OrchestratorState.initial(branch="b", inputs={})
        for name in ostate.PHASE_ORDER:
            st.set_status(name, ostate.OK, input_hash=f"h-{name}")
        st.clear_from("dc_build")
        # Earlier ones still OK.
        self.assertEqual(st.phases["worktree_init"].status, ostate.OK)
        self.assertEqual(st.phases["evg_prepare"].status, ostate.OK)
        # dc_build itself + later are PENDING with cleared hash.
        self.assertEqual(st.phases["dc_build"].status, ostate.PENDING)
        self.assertIsNone(st.phases["dc_build"].input_hash)
        self.assertEqual(st.phases["prepare_e2e"].status, ostate.PENDING)

    def test_clear_from_unknown_phase_raises(self) -> None:
        st = ostate.OrchestratorState.initial(branch="b", inputs={})
        with self.assertRaises(StateConflict):
            st.clear_from("not-a-phase")

    def test_first_non_ok(self) -> None:
        st = ostate.OrchestratorState.initial(branch="b", inputs={})
        for name in ostate.PHASE_ORDER[:3]:
            st.set_status(name, ostate.OK, input_hash="h")
        self.assertEqual(st.first_non_ok(), ostate.PHASE_ORDER[3])
        for name in ostate.PHASE_ORDER:
            st.set_status(name, ostate.OK, input_hash="h")
        self.assertIsNone(st.first_non_ok())


if __name__ == "__main__":
    unittest.main()
