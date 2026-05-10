"""Native Registry tests (Phase 2.5).

Cover:
- round-trip read/write of the registry file
- locking: two threads call ``allocate`` -> distinct prefixes, no corruption
- stale-lock recovery: lock dir owned by a dead PID is force-released
- orphan detection: registry entry without a matching worktree dir flags
  as `stale`; ``prune()`` removes it
- auto-prune on allocate: with all 16 slots occupied but one stale, a
  fresh allocate reclaims the stale slot

All Docker / git interactions are routed through a fake Runner so the
test doesn't depend on the host environment.
"""

from __future__ import annotations

import os
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import patch

from _common import FakePopenFactory, fake_which  # noqa: E402

from wt_ctl.domains.network import (  # noqa: E402
    Registry,
    VALID_RANGE_HI,
    VALID_RANGE_LO,
)
from wt_ctl.errors import RegistryError  # noqa: E402
from wt_ctl.runner import Runner  # noqa: E402


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

class _MakeFakes:
    """Minimal Runner that fakes docker + git calls deterministically.

    - `docker network ls --format {{.Name}}` -> empty (no docker nets)
    - `docker network inspect <name> --format ...` -> always rc=1
    - `git -C <repo> worktree list --porcelain` -> empty
    """

    def runner(self) -> Runner:
        fake = FakePopenFactory(
            mapping={
                ("docker", "network", "ls", "--format", "{{.Name}}"):
                    ("", "", 0),
            },
            default=("", "", 0),
        )
        return Runner(popen_factory=fake, which=fake_which)


def _patch_registry_dir(tmp: Path):
    """Point the module's `_registry_dir` lookup at a tmp dir."""
    return patch.dict(os.environ, {"MCK_DEVC_REGISTRY_DIR": str(tmp)})


# ---------------------------------------------------------------------------
# round-trip
# ---------------------------------------------------------------------------

class RoundTripTests(unittest.TestCase):
    def test_write_then_read(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                # Make matching worktree dirs so entries are "active".
                (wt_parent / "alpha").mkdir()
                (wt_parent / "beta").mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([("alpha", 16), ("beta", 17)])
                rows = reg._read()
                self.assertEqual(rows, [("alpha", 16), ("beta", 17)])
                entries, _ = reg.list()
                self.assertEqual([(e.branch_dir, e.prefix, e.status) for e in entries],
                                 [("alpha", 16, "active"), ("beta", 17, "active")])


# ---------------------------------------------------------------------------
# locking — concurrent allocate
# ---------------------------------------------------------------------------

class LockTests(unittest.TestCase):
    def test_concurrent_allocate_serializes(self) -> None:
        """Two threads racing on allocate() must:
        (a) both succeed,
        (b) get distinct prefixes,
        (c) leave the registry with two valid entries.
        """
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                runner = _MakeFakes().runner()

                results: dict[str, int] = {}
                errors: list[BaseException] = []

                def alloc(name: str) -> None:
                    try:
                        # Make matching worktree dir so the entry isn't
                        # auto-pruned on the next call.
                        (wt_parent / name).mkdir(exist_ok=True)
                        reg = Registry(runner, worktree_parent=wt_parent)
                        results[name] = reg.allocate(
                            branch_dir=name,
                            auto_prune=False,
                            emit_warning=lambda _m: None,
                        )
                    except BaseException as exc:
                        errors.append(exc)

                t1 = threading.Thread(target=alloc, args=("first",))
                t2 = threading.Thread(target=alloc, args=("second",))
                t1.start(); t2.start()
                t1.join(); t2.join()
                self.assertFalse(errors, msg=f"thread raised: {errors}")
                self.assertEqual(set(results.keys()), {"first", "second"})
                self.assertEqual(len(set(results.values())), 2,
                                 msg=f"expected distinct prefixes; got {results}")
                # both prefixes in valid range
                for v in results.values():
                    self.assertGreaterEqual(v, VALID_RANGE_LO)
                    self.assertLessEqual(v, VALID_RANGE_HI)
                # registry still valid (two lines, no garbage)
                rows = Registry(runner, worktree_parent=wt_parent)._read()
                self.assertEqual(len(rows), 2)
                self.assertEqual(set(bd for bd, _ in rows), {"first", "second"})


# ---------------------------------------------------------------------------
# stale-lock recovery
# ---------------------------------------------------------------------------

class StaleLockTests(unittest.TestCase):
    def test_stale_lock_force_released(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                runner = _MakeFakes().runner()

                # Create the lock dir with a known-dead PID.
                lock = Path(td) / "net-prefix-registry.lock.d"
                lock.mkdir(parents=True)
                # PID 999999999 is essentially guaranteed not to exist; if
                # it does, this test would (correctly) fail-loudly so a
                # human can pick a different fixture.
                (lock / "pid").write_text("999999999\n")

                reg = Registry(runner, worktree_parent=wt_parent)
                msgs: list[str] = []
                pref = reg.allocate(
                    branch_dir="recovered",
                    auto_prune=False,
                    emit_warning=msgs.append,
                )
                self.assertGreaterEqual(pref, VALID_RANGE_LO)
                # The entry was actually written.
                rows = reg._read()
                self.assertIn(("recovered", pref), rows)
                # The lock dir is gone (we cleaned up our own lock on exit).
                self.assertFalse(lock.is_dir())
                # The force-release was announced via the warn callback,
                # not silently. (Important for diagnostic visibility in
                # production.)
                self.assertTrue(any("force-releasing stale lock" in m for m in msgs),
                                msg=f"expected force-release warning; got {msgs}")


# ---------------------------------------------------------------------------
# orphan detection + prune
# ---------------------------------------------------------------------------

class OrphanTests(unittest.TestCase):
    def test_list_flags_stale_and_prune_removes(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                # `live` has a directory; `dead` does not.
                (wt_parent / "live").mkdir()

                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([("live", 16), ("dead", 17)])

                entries, _ = reg.list()
                kinds = {e.branch_dir: e.status for e in entries}
                self.assertEqual(kinds, {"live": "active", "dead": "stale"})

                out = reg.prune()
                self.assertIn("dead=17", out)
                rows_after = reg._read()
                self.assertEqual(rows_after, [("live", 16)])


# ---------------------------------------------------------------------------
# allocate auto-prune
# ---------------------------------------------------------------------------

class AutoPruneTests(unittest.TestCase):
    def test_allocate_auto_reclaims_stale_slot(self) -> None:
        """All 16 slots full but one entry is stale; a fresh allocate
        reclaims that exact slot.
        """
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()

                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)

                # Fill all slots [16..31]; mark slot 24 stale (no dir);
                # all others have matching dirs.
                rows: list[tuple[str, int]] = []
                for p in range(VALID_RANGE_LO, VALID_RANGE_HI + 1):
                    bd = f"slot-{p}"
                    if p != 24:
                        (wt_parent / bd).mkdir()
                    rows.append((bd, p))
                reg._write(rows)

                pref = reg.allocate(
                    branch_dir="newcomer", auto_prune=True,
                    emit_warning=lambda _m: None,
                )
                self.assertEqual(pref, 24)
                rows_after = reg._read()
                # slot-24 is gone, newcomer takes 24
                self.assertNotIn(("slot-24", 24), rows_after)
                self.assertIn(("newcomer", 24), rows_after)

    def test_allocate_exhausts_when_no_room(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                rows: list[tuple[str, int]] = []
                for p in range(VALID_RANGE_LO, VALID_RANGE_HI + 1):
                    bd = f"slot-{p}"
                    (wt_parent / bd).mkdir()
                    rows.append((bd, p))
                reg._write(rows)
                with self.assertRaises(RegistryError):
                    reg.allocate(branch_dir="too-late")


# ---------------------------------------------------------------------------
# release
# ---------------------------------------------------------------------------

class ReleaseTests(unittest.TestCase):
    def test_release_drops_matching_entry(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                (wt_parent / "alpha").mkdir()
                (wt_parent / "beta").mkdir()

                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([("alpha", 16), ("beta", 17)])
                out = reg.release("alpha")
                self.assertIn("Released alpha=16", out)
                self.assertEqual(reg._read(), [("beta", 17)])

    def test_release_unknown_is_noop(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([("alpha", 16)])
                out = reg.release("nope")
                self.assertIn("nothing to release", out)
                self.assertEqual(reg._read(), [("alpha", 16)])


if __name__ == "__main__":
    unittest.main()
