"""Native Registry tests (Phase 2.5 + /23 expansion).

Cover:
- index -> StackParams math: round-trip on boundary indices
- /23 allocator: empty registry → first free index, allocator skips
  legacy /16 blocked second octets, mixed legacy/new round-trip
- migrate_legacy_env: legacy `.env` gets four derived vars; idempotent
- locking: two threads call ``allocate`` -> distinct indices, no corruption
- stale-lock recovery: lock dir owned by a dead PID is force-released
- orphan detection: registry entry without a matching worktree dir flags
  as `stale`; ``prune()`` removes it
- auto-prune on allocate: a stale entry is auto-reclaimed

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
    DERIVED_ENV_KEYS,
    INDEX_HI,
    INDEX_LO,
    LEGACY_X_HI,
    LEGACY_X_LO,
    Registry,
    StackParams,
    VALID_RANGE_HI,
    VALID_RANGE_LO,
    _RegistryRow,
    _format_rhs,
    _parse_rhs,
    env_lines_for,
    migrate_legacy_env,
    stack_params,
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
# math: stack_params + boundary indices
# ---------------------------------------------------------------------------

class StackParamsMathTests(unittest.TestCase):
    """Confirm the index -> X/Y/PORT mapping at every load-bearing boundary."""

    def _check(self, n: int, x: int, y_base: int, y_vip: int, port: int) -> None:
        p = stack_params(n, scheme="new")
        self.assertEqual(p.index, n)
        self.assertEqual(p.x, x)
        self.assertEqual(p.y_base, y_base)
        self.assertEqual(p.y_vip, y_vip)
        self.assertEqual(p.port, port)
        self.assertEqual(p.scheme, "new")
        # Subnet/ip_range/vip_cidr/proxy_address are derived consistently.
        self.assertEqual(p.subnet, f"172.{x}.{y_base}.0/23")
        self.assertEqual(p.ip_range, f"172.{x}.{y_base}.0/24")
        self.assertEqual(p.vip_cidr, f"172.{x}.{y_vip}.0/24")
        self.assertEqual(p.proxy_address, f"172.{x}.{y_base}.10")

    def test_n0(self) -> None:
        # Smallest index: X=16, Y_BASE=0, Y_VIP=1, PORT=8000.
        self._check(0, x=16, y_base=0, y_vip=1, port=8000)

    def test_n127_last_in_x16(self) -> None:
        # Last index whose X==16: (127 >> 7) == 0, (127 & 0x7F) << 1 == 254.
        self._check(127, x=16, y_base=254, y_vip=255, port=8127)

    def test_n128_first_in_x17(self) -> None:
        # First index whose X==17: (128 >> 7) == 1, (128 & 0x7F) << 1 == 0.
        self._check(128, x=17, y_base=0, y_vip=1, port=8128)

    def test_n1023(self) -> None:
        # Mid-range: (1023 >> 7) == 7, (1023 & 0x7F) == 127, *2 == 254.
        self._check(1023, x=23, y_base=254, y_vip=255, port=9023)

    def test_n2047_max(self) -> None:
        # Largest index: X=31, Y_BASE=254, Y_VIP=255, PORT=10047.
        self._check(2047, x=31, y_base=254, y_vip=255, port=10047)

    def test_out_of_range_raises(self) -> None:
        with self.assertRaises(RegistryError):
            stack_params(-1, scheme="new")
        with self.assertRaises(RegistryError):
            stack_params(INDEX_HI + 1, scheme="new")

    def test_legacy_scheme_pinned_values(self) -> None:
        for nn in (LEGACY_X_LO, 24, LEGACY_X_HI):
            p = stack_params(nn, scheme="legacy")
            self.assertEqual(p.x, nn)
            self.assertEqual(p.y_base, 0)
            self.assertEqual(p.y_vip, 1)
            self.assertEqual(p.port, 8000 + nn)
            self.assertEqual(p.subnet, f"172.{nn}.0.0/16")

    def test_legacy_out_of_range_raises(self) -> None:
        with self.assertRaises(RegistryError):
            stack_params(15, scheme="legacy")
        with self.assertRaises(RegistryError):
            stack_params(32, scheme="legacy")


# ---------------------------------------------------------------------------
# row parsing/formatting round-trip
# ---------------------------------------------------------------------------

class RowFormatTests(unittest.TestCase):
    def test_legacy_bare_int_round_trip(self) -> None:
        row = _parse_rhs("foo", "24")
        assert row is not None
        self.assertEqual(row.params.scheme, "legacy")
        self.assertEqual(row.params.x, 24)
        self.assertEqual(_format_rhs(row.params), "24")

    def test_new_composite_round_trip(self) -> None:
        # N=128 -> X=17, Y_BASE=0, Y_VIP=1, PORT=8128
        row = _parse_rhs("bar", "128:17:0:1:8128")
        assert row is not None
        self.assertEqual(row.params.scheme, "new")
        self.assertEqual(row.params.index, 128)
        self.assertEqual(_format_rhs(row.params), "128:17:0:1:8128")

    def test_malformed_drops(self) -> None:
        self.assertIsNone(_parse_rhs("x", "not-a-number"))
        self.assertIsNone(_parse_rhs("x", "9999"))   # outside legacy band
        self.assertIsNone(_parse_rhs("x", "1:2:3"))  # wrong arity
        self.assertIsNone(_parse_rhs("x", "12"))     # outside legacy band

    def test_inconsistent_composite_falls_back_to_derived(self) -> None:
        # Claim N=0 (which derives X=16) but write X=99 — registry must
        # warn and fall back to the derived values rather than route
        # traffic to a phantom subnet.
        row = _parse_rhs("zz", "0:99:0:1:8000")
        assert row is not None
        self.assertEqual(row.params.x, 16)


# ---------------------------------------------------------------------------
# round-trip — write then read with mixed schemes
# ---------------------------------------------------------------------------

class RoundTripTests(unittest.TestCase):
    def test_write_then_read_mixed(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                # Make matching worktree dirs so entries are "active".
                for bd in ("alpha", "beta", "gamma", "delta"):
                    (wt_parent / bd).mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                rows = [
                    _RegistryRow("alpha", stack_params(24, scheme="legacy")),
                    _RegistryRow("beta", stack_params(28, scheme="legacy")),
                    _RegistryRow("gamma", stack_params(0, scheme="new")),
                    _RegistryRow("delta", stack_params(1023, scheme="new")),
                ]
                reg._write(rows)
                rows_back = reg._read()
                self.assertEqual(
                    [(r.branch_dir, r.params.index, r.params.scheme) for r in rows_back],
                    [
                        ("alpha", 24, "legacy"),
                        ("beta", 28, "legacy"),
                        ("gamma", 0, "new"),
                        ("delta", 1023, "new"),
                    ],
                )
                entries, _ = reg.list()
                # NetEntry.prefix carries the second octet for legacy (24/28)
                # and the index for new (0/1023). All four are "active"
                # because their dirs exist.
                self.assertEqual(
                    [(e.branch_dir, e.prefix, e.status) for e in entries],
                    [
                        ("alpha", 24, "active"),
                        ("beta", 28, "active"),
                        ("gamma", 0, "active"),
                        ("delta", 1023, "active"),
                    ],
                )


# ---------------------------------------------------------------------------
# allocate — empty registry hits index 0
# ---------------------------------------------------------------------------

class AllocateTests(unittest.TestCase):
    def test_empty_registry_first_index(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                (wt_parent / "newcomer").mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                params = reg.allocate(
                    branch_dir="newcomer",
                    auto_prune=False,
                    emit_warning=lambda _m: None,
                )
                self.assertEqual(params.index, 0)
                self.assertEqual(params.x, 16)
                self.assertEqual(params.scheme, "new")

    def test_allocator_skips_x_blocked_by_legacy_entry(self) -> None:
        """A legacy entry at X=24 must block all 128 indices whose X==24."""
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                # Pre-fill: legacy /16 owner at X=24 + every non-X=24 index
                # in 0..1023 occupied (so the allocator must scan past them).
                # Simpler: mark every X != 24 candidate up to the threshold
                # by occupying a single blocking legacy entry, and also
                # block X=16 with a legacy entry, then check the next free.
                (wt_parent / "legacy_24").mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([
                    _RegistryRow("legacy_24", stack_params(24, scheme="legacy")),
                ])
                (wt_parent / "newcomer").mkdir()
                params = reg.allocate(
                    branch_dir="newcomer",
                    auto_prune=False,
                    emit_warning=lambda _m: None,
                )
                # Index 0 is X=16 (not blocked), so allocator returns 0.
                self.assertEqual(params.index, 0)
                self.assertEqual(params.x, 16)
                # Now legacy_24 blocks X=24. Force the allocator to scan
                # into the X=24 band by occupying every preceding index.
                reg._write([
                    _RegistryRow("legacy_24", stack_params(24, scheme="legacy")),
                    *[
                        _RegistryRow(f"slot_{n}", stack_params(n, scheme="new"))
                        for n in range(0, 1024) if (16 + (n >> 7)) != 24
                    ],
                ])
                # Make matching worktree dirs so they don't auto-prune.
                for n in range(0, 1024):
                    if (16 + (n >> 7)) == 24:
                        continue
                    p = wt_parent / f"slot_{n}"
                    if not p.exists():
                        p.mkdir()
                params = reg.allocate(
                    branch_dir="post_block",
                    auto_prune=False,
                    emit_warning=lambda _m: None,
                )
                # Every X in [16,23] is exhausted; X=24 is blocked; first
                # free index lives at X=25 (which is index 128 * 9 = 1152).
                # 1152 >> 7 == 9; X = 16 + 9 == 25.
                self.assertEqual(params.x, 25)
                self.assertEqual(params.index, 1152)


# ---------------------------------------------------------------------------
# mixed legacy / new round-trip via list + release + re-allocate
# ---------------------------------------------------------------------------

class MixedRoundTripTests(unittest.TestCase):
    def test_two_legacy_three_new_then_release_and_realloc(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                names = ["leg22", "leg30", "new0", "new200", "new300"]
                for bd in names:
                    (wt_parent / bd).mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([
                    _RegistryRow("leg22", stack_params(22, scheme="legacy")),
                    _RegistryRow("leg30", stack_params(30, scheme="legacy")),
                    _RegistryRow("new0", stack_params(0, scheme="new")),
                    _RegistryRow("new200", stack_params(200, scheme="new")),
                    _RegistryRow("new300", stack_params(300, scheme="new")),
                ])
                rows = reg._read()
                schemes = {r.branch_dir: r.params.scheme for r in rows}
                self.assertEqual(schemes, {
                    "leg22": "legacy", "leg30": "legacy",
                    "new0": "new", "new200": "new", "new300": "new",
                })
                # Release each cleanly and confirm the line is gone.
                for bd in names:
                    reg.release(bd)
                self.assertEqual(reg._read(), [])
                # Now re-allocate; the freed indices come back in order.
                (wt_parent / "rebooked").mkdir()
                params = reg.allocate(
                    branch_dir="rebooked",
                    auto_prune=False,
                    emit_warning=lambda _m: None,
                )
                self.assertEqual(params.index, 0)


# ---------------------------------------------------------------------------
# migrate_legacy_env
# ---------------------------------------------------------------------------

class MigrateLegacyEnvTests(unittest.TestCase):
    def test_legacy_env_gets_four_derived_vars(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            env = Path(td) / ".env"
            env.write_text("MCK_DEVC_NET_PREFIX=24\n")
            changed, msg = migrate_legacy_env(env)
            self.assertTrue(changed, msg=msg)
            text = env.read_text()
            for k in DERIVED_ENV_KEYS:
                self.assertIn(f"{k}=", text, msg=f"missing {k} in {text!r}")
            # legacy 24 -> X=24, Y_BASE=0, Y_VIP=1, PORT=8024
            self.assertIn("MCK_DEVC_NET_X=24\n", text)
            self.assertIn("MCK_DEVC_NET_Y_BASE=0\n", text)
            self.assertIn("MCK_DEVC_NET_Y_VIP=1\n", text)
            self.assertIn("MCK_DEVC_PROXY_PORT=8024\n", text)
            # Each derived var appears exactly once.
            for k in DERIVED_ENV_KEYS:
                self.assertEqual(text.count(f"{k}="), 1, msg=f"{k} duplicated: {text!r}")

    def test_idempotent(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            env = Path(td) / ".env"
            env.write_text("MCK_DEVC_NET_PREFIX=24\n")
            migrate_legacy_env(env)
            first = env.read_text()
            changed, msg = migrate_legacy_env(env)
            self.assertFalse(changed, msg=msg)
            self.assertEqual(env.read_text(), first)

    def test_new_scheme_env_top_up(self) -> None:
        """A .env with a new-scheme prefix (e.g. N=200) but missing some
        derived vars gets topped up; an already-complete .env is no-op.
        """
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            env = Path(td) / ".env"
            env.write_text("MCK_DEVC_NET_PREFIX=200\n")
            changed, _ = migrate_legacy_env(env)
            self.assertTrue(changed)
            params = stack_params(200, scheme="new")
            text = env.read_text()
            self.assertIn(f"MCK_DEVC_NET_X={params.x}\n", text)
            self.assertIn(f"MCK_DEVC_NET_Y_BASE={params.y_base}\n", text)
            self.assertIn(f"MCK_DEVC_NET_Y_VIP={params.y_vip}\n", text)
            self.assertIn(f"MCK_DEVC_PROXY_PORT={params.port}\n", text)

    def test_missing_env_is_noop(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            env = Path(td) / "nonexistent.env"
            changed, msg = migrate_legacy_env(env)
            self.assertFalse(changed)


# ---------------------------------------------------------------------------
# env_lines_for
# ---------------------------------------------------------------------------

class EnvLinesTests(unittest.TestCase):
    def test_new_scheme_block(self) -> None:
        params = stack_params(0, scheme="new")
        lines = env_lines_for(params)
        self.assertEqual(lines, [
            "MCK_DEVC_NET_PREFIX=0",
            "MCK_DEVC_NET_X=16",
            "MCK_DEVC_NET_Y_BASE=0",
            "MCK_DEVC_NET_Y_VIP=1",
            "MCK_DEVC_PROXY_PORT=8000",
        ])

    def test_legacy_scheme_block(self) -> None:
        params = stack_params(24, scheme="legacy")
        lines = env_lines_for(params)
        # MCK_DEVC_NET_PREFIX is the index field; for legacy that's the
        # second octet (24).
        self.assertEqual(lines[0], "MCK_DEVC_NET_PREFIX=24")
        self.assertEqual(lines[1], "MCK_DEVC_NET_X=24")
        self.assertEqual(lines[2], "MCK_DEVC_NET_Y_BASE=0")
        self.assertEqual(lines[3], "MCK_DEVC_NET_Y_VIP=1")
        self.assertEqual(lines[4], "MCK_DEVC_PROXY_PORT=8024")


# ---------------------------------------------------------------------------
# locking — concurrent allocate
# ---------------------------------------------------------------------------

class LockTests(unittest.TestCase):
    def test_concurrent_allocate_serializes(self) -> None:
        """Two threads racing on allocate() must:
        (a) both succeed,
        (b) get distinct indices,
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
                        params = reg.allocate(
                            branch_dir=name,
                            auto_prune=False,
                            emit_warning=lambda _m: None,
                        )
                        results[name] = params.index
                    except BaseException as exc:
                        errors.append(exc)

                t1 = threading.Thread(target=alloc, args=("first",))
                t2 = threading.Thread(target=alloc, args=("second",))
                t1.start(); t2.start()
                t1.join(); t2.join()
                self.assertFalse(errors, msg=f"thread raised: {errors}")
                self.assertEqual(set(results.keys()), {"first", "second"})
                self.assertEqual(len(set(results.values())), 2,
                                 msg=f"expected distinct indices; got {results}")
                # both indices in valid range
                for v in results.values():
                    self.assertGreaterEqual(v, INDEX_LO)
                    self.assertLessEqual(v, INDEX_HI)
                # registry still valid (two lines, no garbage)
                rows = Registry(runner, worktree_parent=wt_parent)._read()
                self.assertEqual(len(rows), 2)
                self.assertEqual({r.branch_dir for r in rows}, {"first", "second"})


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
                params = reg.allocate(
                    branch_dir="recovered",
                    auto_prune=False,
                    emit_warning=msgs.append,
                )
                self.assertGreaterEqual(params.index, INDEX_LO)
                # The entry was actually written.
                rows = reg._read()
                bds = [r.branch_dir for r in rows]
                self.assertIn("recovered", bds)
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
                reg._write([
                    _RegistryRow("live", stack_params(0, scheme="new")),
                    _RegistryRow("dead", stack_params(1, scheme="new")),
                ])

                entries, _ = reg.list()
                kinds = {e.branch_dir: e.status for e in entries}
                self.assertEqual(kinds, {"live": "active", "dead": "stale"})

                out = reg.prune()
                self.assertIn("dead=", out)
                rows_after = reg._read()
                self.assertEqual(len(rows_after), 1)
                self.assertEqual(rows_after[0].branch_dir, "live")


# ---------------------------------------------------------------------------
# allocate auto-prune
# ---------------------------------------------------------------------------

class AutoPruneTests(unittest.TestCase):
    def test_allocate_auto_reclaims_stale_slot(self) -> None:
        """Index 0 occupied but the holding worktree is gone (stale); a
        fresh allocate auto-prunes that slot and reclaims it.
        """
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()

                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)

                # Occupy index 0 with a stale entry (no matching dir).
                reg._write([_RegistryRow("ghost", stack_params(0, scheme="new"))])

                (wt_parent / "newcomer").mkdir()
                params = reg.allocate(
                    branch_dir="newcomer", auto_prune=True,
                    emit_warning=lambda _m: None,
                )
                self.assertEqual(params.index, 0)
                rows_after = reg._read()
                bds = {r.branch_dir for r in rows_after}
                self.assertNotIn("ghost", bds)
                self.assertIn("newcomer", bds)


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
                reg._write([
                    _RegistryRow("alpha", stack_params(0, scheme="new")),
                    _RegistryRow("beta", stack_params(1, scheme="new")),
                ])
                out = reg.release("alpha")
                self.assertIn("Released alpha=", out)
                rows = reg._read()
                self.assertEqual(len(rows), 1)
                self.assertEqual(rows[0].branch_dir, "beta")

    def test_release_legacy_entry(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                (wt_parent / "leg").mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([_RegistryRow("leg", stack_params(24, scheme="legacy"))])
                out = reg.release("leg")
                # The bare-int form round-trips for legacy.
                self.assertIn("Released leg=24", out)
                self.assertEqual(reg._read(), [])

    def test_release_unknown_is_noop(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([_RegistryRow("alpha", stack_params(0, scheme="new"))])
                out = reg.release("nope")
                self.assertIn("nothing to release", out)
                rows = reg._read()
                self.assertEqual(len(rows), 1)


if __name__ == "__main__":
    unittest.main()
