"""Registry tests: index→StackParams math, /23 allocation, flock locking,
orphan detection, and prune. Docker/git go through a fake Runner so the
tests don't depend on the host environment.
"""

from __future__ import annotations

import os
import threading
import unittest
from pathlib import Path
from unittest.mock import patch

from _common import FakePopenFactory, fake_which  # noqa: E402
from wt_ctl.domains.network import (  # noqa: E402
    DERIVED_ENV_KEYS,
    INDEX_HI,
    INDEX_LO,
    VALID_RANGE_HI,
    VALID_RANGE_LO,
    Registry,
    StackParams,
    _format_rhs,
    _parse_rhs,
    _RegistryRow,
    env_lines_for,
    stack_params,
)
from wt_ctl.errors import LockTimeout, RegistryError  # noqa: E402
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
                ("docker", "network", "ls", "--format", "{{.Name}}"): ("", "", 0),
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
        p = stack_params(n)
        self.assertEqual(p.index, n)
        self.assertEqual(p.x, x)
        self.assertEqual(p.y_base, y_base)
        self.assertEqual(p.y_vip, y_vip)
        self.assertEqual(p.port, port)
        self.assertEqual(p.subnet, f"172.{x}.{y_base}.0/23")
        self.assertEqual(p.ip_range, f"172.{x}.{y_base}.0/24")
        self.assertEqual(p.vip_cidr, f"172.{x}.{y_vip}.0/24")
        self.assertEqual(p.proxy_address, f"172.{x}.{y_base}.10")

    def test_n0(self) -> None:
        self._check(0, x=16, y_base=0, y_vip=1, port=8000)

    def test_n127_last_in_x16(self) -> None:
        self._check(127, x=16, y_base=254, y_vip=255, port=8127)

    def test_n128_first_in_x17(self) -> None:
        self._check(128, x=17, y_base=0, y_vip=1, port=8128)

    def test_n1023(self) -> None:
        self._check(1023, x=23, y_base=254, y_vip=255, port=9023)

    def test_n2047_max(self) -> None:
        self._check(2047, x=31, y_base=254, y_vip=255, port=10047)

    def test_out_of_range_raises(self) -> None:
        with self.assertRaises(RegistryError):
            stack_params(-1)
        with self.assertRaises(RegistryError):
            stack_params(INDEX_HI + 1)


# ---------------------------------------------------------------------------
# row parsing/formatting round-trip
# ---------------------------------------------------------------------------


class RowFormatTests(unittest.TestCase):
    def test_composite_round_trip(self) -> None:
        # N=128 -> X=17, Y_BASE=0, Y_VIP=1, PORT=8128
        row = _parse_rhs("bar", "128:17:0:1:8128")
        assert row is not None
        self.assertEqual(row.params.index, 128)
        self.assertEqual(_format_rhs(row.params), "128:17:0:1:8128")

    def test_malformed_drops(self) -> None:
        self.assertIsNone(_parse_rhs("x", "not-a-number"))
        self.assertIsNone(_parse_rhs("x", "1:2:3"))  # wrong arity
        self.assertIsNone(_parse_rhs("x", "24"))  # bare-int form (rejected)
        self.assertIsNone(_parse_rhs("x", "9999"))  # out of index range
        self.assertIsNone(_parse_rhs("x", "9999:1:2:3:4"))

    def test_inconsistent_composite_falls_back_to_derived(self) -> None:
        # Claim N=0 (which derives X=16) but write X=99 — registry must
        # warn and fall back to the derived values rather than route
        # traffic to a phantom subnet.
        row = _parse_rhs("zz", "0:99:0:1:8000")
        assert row is not None
        self.assertEqual(row.params.x, 16)


# ---------------------------------------------------------------------------
# round-trip — write then read
# ---------------------------------------------------------------------------


class RoundTripTests(unittest.TestCase):
    def test_write_then_read(self) -> None:
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
                    _RegistryRow("alpha", stack_params(0)),
                    _RegistryRow("beta", stack_params(127)),
                    _RegistryRow("gamma", stack_params(128)),
                    _RegistryRow("delta", stack_params(1023)),
                ]
                reg._write(rows)
                rows_back = reg._read()
                self.assertEqual(
                    [(r.branch_dir, r.params.index) for r in rows_back],
                    [
                        ("alpha", 0),
                        ("beta", 127),
                        ("gamma", 128),
                        ("delta", 1023),
                    ],
                )
                entries, _ = reg.list()
                self.assertEqual(
                    [(e.branch_dir, e.prefix, e.status) for e in entries],
                    [
                        ("alpha", 0, "active"),
                        ("beta", 127, "active"),
                        ("gamma", 128, "active"),
                        ("delta", 1023, "active"),
                    ],
                )


# ---------------------------------------------------------------------------
# allocate
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

    def test_allocator_skips_blocked_x_octet(self) -> None:
        """A foreign /16 docker network at X=16 (e.g. kind) overlaps every
        /23 in that octet, so all 128 indices with X==16 are blocked and the
        allocator returns the first index in X=17.
        """
        import tempfile

        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                (wt_parent / "newcomer").mkdir()

                # Fake a docker network at 172.16.0.0/16 (e.g. "kind").
                fake = FakePopenFactory(
                    mapping={
                        ("docker", "network", "ls", "--format", "{{.Name}}"): ("kind\n", "", 0),
                        (
                            "docker",
                            "network",
                            "inspect",
                            "kind",
                            "--format",
                            "{{range .IPAM.Config}}{{.Subnet}}{{end}}",
                        ): ("172.16.0.0/16", "", 0),
                    },
                    default=("", "", 0),
                )
                runner = Runner(popen_factory=fake, which=fake_which)
                reg = Registry(runner, worktree_parent=wt_parent)
                params = reg.allocate(
                    branch_dir="newcomer",
                    auto_prune=False,
                    emit_warning=lambda _m: None,
                )
                # X=16 is blocked, so first free index is 128 (X=17, Y_BASE=0).
                self.assertEqual(params.x, 17)
                self.assertEqual(params.index, 128)

    def test_sibling_slash23_blocks_only_its_slot(self) -> None:
        """A sibling /23 compose network at 172.16.0.0/23 occupies only index
        0's slot; the allocator must stay in X=16 and hand out index 1
        (172.16.2.0/23), not skip the whole octet.
        """
        import tempfile

        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                (wt_parent / "newcomer").mkdir()

                fake = FakePopenFactory(
                    mapping={
                        ("docker", "network", "ls", "--format", "{{.Name}}"): (
                            "sibling_devcontainer_devcontainer\n",
                            "",
                            0,
                        ),
                        (
                            "docker",
                            "network",
                            "inspect",
                            "sibling_devcontainer_devcontainer",
                            "--format",
                            "{{range .IPAM.Config}}{{.Subnet}}{{end}}",
                        ): ("172.16.0.0/23", "", 0),
                    },
                    default=("", "", 0),
                )
                runner = Runner(popen_factory=fake, which=fake_which)
                reg = Registry(runner, worktree_parent=wt_parent)
                params = reg.allocate(
                    branch_dir="newcomer",
                    auto_prune=False,
                    emit_warning=lambda _m: None,
                )
                self.assertEqual(params.x, 16)
                self.assertEqual(params.index, 1)
                self.assertEqual(params.subnet, "172.16.2.0/23")


# ---------------------------------------------------------------------------
# release + re-allocate round-trip
# ---------------------------------------------------------------------------


class ReleaseRoundTripTests(unittest.TestCase):
    def test_release_frees_index_for_reallocation(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                names = ["a0", "a200", "a300"]
                for bd in names:
                    (wt_parent / bd).mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write(
                    [
                        _RegistryRow("a0", stack_params(0)),
                        _RegistryRow("a200", stack_params(200)),
                        _RegistryRow("a300", stack_params(300)),
                    ]
                )
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
# env_lines_for
# ---------------------------------------------------------------------------


class EnvLinesTests(unittest.TestCase):
    def test_index_zero_block(self) -> None:
        params = stack_params(0)
        lines = env_lines_for(params)
        self.assertEqual(
            lines,
            [
                "MCK_DEVC_NET_PREFIX=0",
                "MCK_DEVC_NET_X=16",
                "MCK_DEVC_NET_Y_BASE=0",
                "MCK_DEVC_NET_Y_VIP=1",
                "MCK_DEVC_PROXY_PORT=8000",
            ],
        )

    def test_mid_range_block(self) -> None:
        params = stack_params(200)
        # 200 >> 7 == 1 -> X=17; 200 & 0x7F == 72; 72*2 == 144
        self.assertEqual(
            env_lines_for(params),
            [
                "MCK_DEVC_NET_PREFIX=200",
                "MCK_DEVC_NET_X=17",
                "MCK_DEVC_NET_Y_BASE=144",
                "MCK_DEVC_NET_Y_VIP=145",
                "MCK_DEVC_PROXY_PORT=8200",
            ],
        )

    def test_derived_keys_present(self) -> None:
        for k in DERIVED_ENV_KEYS:
            self.assertTrue(k.startswith("MCK_DEVC_"))


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
                t1.start()
                t2.start()
                t1.join()
                t2.join()
                self.assertFalse(errors, msg=f"thread raised: {errors}")
                self.assertEqual(set(results.keys()), {"first", "second"})
                self.assertEqual(len(set(results.values())), 2, msg=f"expected distinct indices; got {results}")
                # both indices in valid range
                for v in results.values():
                    self.assertGreaterEqual(v, INDEX_LO)
                    self.assertLessEqual(v, INDEX_HI)
                # registry still valid (two lines, no garbage)
                rows = Registry(runner, worktree_parent=wt_parent)._read()
                self.assertEqual(len(rows), 2)
                self.assertEqual({r.branch_dir for r in rows}, {"first", "second"})


# ---------------------------------------------------------------------------
# lock contention — a held (live) lock must not be stolen
# ---------------------------------------------------------------------------


class LockContentionTests(unittest.TestCase):
    def test_held_lock_blocks_second_acquirer(self) -> None:
        """While one holder is inside ``_registry_lock``, a second acquirer
        must wait — never steal the lock. The flock scheme has no stale
        heuristic, so a live lock is always honored until released."""
        import tempfile

        from wt_ctl.domains.network import _registry_lock

        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                acquired_first = threading.Event()
                release_first = threading.Event()
                order: list[str] = []

                def holder() -> None:
                    with _registry_lock():
                        order.append("first-acquired")
                        acquired_first.set()
                        # Hold until the main thread has proven it can't steal.
                        release_first.wait(timeout=5)
                        order.append("first-released")

                t = threading.Thread(target=holder)
                t.start()
                self.assertTrue(acquired_first.wait(timeout=5), msg="holder never acquired the lock")

                # The lock is held by the live holder thread; a short-timeout
                # acquire must time out rather than proceed.
                with self.assertRaises(LockTimeout):
                    with _registry_lock(timeout=0.5):
                        order.append("STOLE-LOCK")  # pragma: no cover

                # Once released, acquisition succeeds.
                release_first.set()
                t.join(timeout=5)
                with _registry_lock(timeout=5):
                    order.append("second-acquired")

                self.assertNotIn("STOLE-LOCK", order)
                self.assertEqual(order[0], "first-acquired")
                self.assertIn("first-released", order)
                self.assertEqual(order[-1], "second-acquired")


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
                reg._write(
                    [
                        _RegistryRow("live", stack_params(0)),
                        _RegistryRow("dead", stack_params(1)),
                    ]
                )

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
                reg._write([_RegistryRow("ghost", stack_params(0))])

                (wt_parent / "newcomer").mkdir()
                params = reg.allocate(
                    branch_dir="newcomer",
                    auto_prune=True,
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
                reg._write(
                    [
                        _RegistryRow("alpha", stack_params(0)),
                        _RegistryRow("beta", stack_params(1)),
                    ]
                )
                out = reg.release("alpha")
                self.assertIn("Released alpha=", out)
                rows = reg._read()
                self.assertEqual(len(rows), 1)
                self.assertEqual(rows[0].branch_dir, "beta")

    def test_release_unknown_is_noop(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as td:
            with _patch_registry_dir(Path(td)):
                wt_parent = Path(td) / "mdb"
                wt_parent.mkdir()
                runner = _MakeFakes().runner()
                reg = Registry(runner, worktree_parent=wt_parent)
                reg._write([_RegistryRow("alpha", stack_params(0))])
                out = reg.release("nope")
                self.assertIn("nothing to release", out)
                rows = reg._read()
                self.assertEqual(len(rows), 1)


if __name__ == "__main__":
    unittest.main()
