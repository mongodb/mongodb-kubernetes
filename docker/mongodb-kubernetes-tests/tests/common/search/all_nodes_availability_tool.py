"""All-nodes availability primitive for MongoDBSearch.

Spatial fan-out tool that drives ``SearchConnectivityTool`` against
EVERY connectable mongod in a MongoDB deployment — every RS member, or
every mongod across every shard. Returns per-node
``NodeAvailabilityResult`` objects so availability tests can express
"is search reachable on every node?" in one call instead of hand-rolled
``for member_index in range(N)`` loops.

Two construction paths:

* ``AllNodesAvailabilityTool.for_replicaset(mdb, members=...)`` — RS,
  uses ``get_rs_search_tester_for_member`` under the hood.
* ``AllNodesAvailabilityTool.for_sharded(mdb, shard_count=..., mongods_per_shard=...)``
  — sharded, uses ``get_shard_mongod_tester``.

The primitive does NOT change the existing per-node tester factories;
it composes on top of them.

Usage
-----

::

    fan = AllNodesAvailabilityTool.for_replicaset(
        mdb, members=3, username="user", password="pass", use_ssl=True,
    )
    results = fan.oneshot_all()
    failed = [r for r in results if not r.oneshot_succeeded]
    assert not failed, f"oneshot failed on: {[r.node_id for r in failed]}"

    paging_results = fan.paging_all(pages=5, batch_size=10)
    for r in paging_results:
        assert r.paging_succeeded, f"{r.node_id}: {r.paging_verdict.as_dict()}"

No mocking — every call goes against real mongods via the underlying
SearchTester. Time-series ("is this connection healthy over a window")
is the job of ``SearchAvailabilityBackgroundTester``; this primitive is
the spatial counterpart.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Iterator, Optional

from kubetester.mongodb import MongoDB
from tests import test_logger
from tests.common.search.connectivity import ConnectivityVerdict, QueryResult, SearchConnectivityTool
from tests.common.search.rs_search_helper import get_rs_search_tester_for_member
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import get_shard_mongod_tester

logger = test_logger.get_test_logger(__name__)


__all__ = [
    "NodeAvailabilityResult",
    "AllNodesAvailabilityTool",
]


TOPOLOGY_RS = "rs"
TOPOLOGY_SHARDED = "sharded"
_TOPOLOGIES = frozenset({TOPOLOGY_RS, TOPOLOGY_SHARDED})


@dataclass
class NodeAvailabilityResult:
    """Per-node verdict from ``AllNodesAvailabilityTool`` fan-out.

    ``oneshot_result`` is populated by ``oneshot_all`` / ``run``;
    ``paging_results`` + ``paging_verdict`` are populated by ``paging_all``
    / ``run``. A run-mode-specific call leaves the other side ``None`` /
    empty so callers can branch on what was actually exercised.
    """

    node_id: str
    oneshot_result: Optional[QueryResult] = None
    paging_results: list[QueryResult] = field(default_factory=list)
    paging_verdict: Optional[ConnectivityVerdict] = None

    @property
    def oneshot_succeeded(self) -> bool:
        """One-shot query succeeded."""
        return bool(self.oneshot_result and self.oneshot_result.success)

    @property
    def paging_succeeded(self) -> bool:
        """Every paging page succeeded (no failures across the verdict)."""
        return bool(self.paging_verdict and self.paging_verdict.failed == 0)


class AllNodesAvailabilityTool:
    """Fan out search-availability probes across every mongod in a SC MongoDB.

    Construction is via ``for_replicaset`` / ``for_sharded`` classmethods —
    the ``__init__`` signature accepts both shapes but the classmethods
    document the intent at the call site.

    The primitive itself is stateless across calls: each
    ``oneshot_all`` / ``paging_all`` builds fresh ``SearchTester`` +
    ``SearchConnectivityTool`` per node, runs the probe, captures the
    result, and lets pymongo socket cleanup happen on scope exit. A
    fresh ``SearchConnectivityTool`` per node is required because
    ``SearchConnectivityTool`` installs a CommandListener at construction
    time and reusing one would conflate wire-op streams across nodes.

    Topology numbers are passed in (not introspected from ``mdb.spec``)
    so tests don't race the operator's spec round-trip and so the
    primitive stays decoupled from the resource-CR schema. The
    bootstrap-mixin config dataclasses (``MongoDBRsDeploymentConfig``,
    ``MongoDBShardedDeploymentConfig``) already carry the numbers, so
    call sites just thread ``cfg.rs_members`` / ``cfg.shard_count`` /
    ``cfg.mongods_per_shard`` through.
    """

    def __init__(
        self,
        mdb: MongoDB,
        *,
        topology: str,
        rs_members: int = 0,
        shard_count: int = 0,
        mongods_per_shard: int = 0,
        username: str,
        password: str,
        use_ssl: bool = True,
        read_preference: str = "secondaryPreferred",
    ) -> None:
        if topology not in _TOPOLOGIES:
            raise ValueError(f"topology must be one of {sorted(_TOPOLOGIES)}; got {topology!r}")
        if topology == TOPOLOGY_RS:
            if rs_members < 1:
                raise ValueError(f"rs_members must be >= 1 for RS topology; got {rs_members}")
        else:  # sharded
            if shard_count < 1:
                raise ValueError(f"shard_count must be >= 1 for sharded topology; got {shard_count}")
            if mongods_per_shard < 1:
                raise ValueError(f"mongods_per_shard must be >= 1 for sharded topology; got {mongods_per_shard}")
        self._mdb = mdb
        self._topology = topology
        self._rs_members = rs_members
        self._shard_count = shard_count
        self._mongods_per_shard = mongods_per_shard
        self._username = username
        self._password = password
        self._use_ssl = use_ssl
        self._read_preference = read_preference

    # ------------------------------------------------------------------
    # Construction
    # ------------------------------------------------------------------

    @classmethod
    def for_replicaset(
        cls,
        mdb: MongoDB,
        members: int,
        *,
        username: str,
        password: str,
        use_ssl: bool = True,
        read_preference: str = "secondaryPreferred",
    ) -> "AllNodesAvailabilityTool":
        """Build a fan-out tool for a single-cluster RS MongoDB."""
        return cls(
            mdb,
            topology=TOPOLOGY_RS,
            rs_members=members,
            username=username,
            password=password,
            use_ssl=use_ssl,
            read_preference=read_preference,
        )

    @classmethod
    def for_sharded(
        cls,
        mdb: MongoDB,
        *,
        shard_count: int,
        mongods_per_shard: int,
        username: str,
        password: str,
        use_ssl: bool = True,
    ) -> "AllNodesAvailabilityTool":
        """Build a fan-out tool for a single-cluster sharded MongoDB."""
        return cls(
            mdb,
            topology=TOPOLOGY_SHARDED,
            shard_count=shard_count,
            mongods_per_shard=mongods_per_shard,
            username=username,
            password=password,
            use_ssl=use_ssl,
        )

    # ------------------------------------------------------------------
    # Topology iteration
    # ------------------------------------------------------------------

    def iter_node_testers(self) -> Iterator[tuple[str, SearchTester]]:
        """Yield ``(node_id, SearchTester)`` for every mongod in topology order.

        Node IDs:

        * RS:      ``"rs/member-{i}"``                     for i in 0..rs_members-1
        * Sharded: ``"shard/{mdb.name}-{s}/member-{m}"``   for s, m

        Each tester is a fresh ``SearchTester`` (no caching) — the
        caller owns lifecycle. ``SearchConnectivityTool`` will close
        any pre-existing client on the SearchTester when it installs
        its CommandListener, so no explicit cleanup is required.
        """
        if self._topology == TOPOLOGY_RS:
            for i in range(self._rs_members):
                tester = get_rs_search_tester_for_member(
                    self._mdb,
                    member_index=i,
                    username=self._username,
                    password=self._password,
                    use_ssl=self._use_ssl,
                    read_preference=self._read_preference,
                )
                yield (f"rs/member-{i}", tester)
            return

        # Sharded
        for shard_index in range(self._shard_count):
            shard_name = f"{self._mdb.name}-{shard_index}"
            for member_index in range(self._mongods_per_shard):
                tester = get_shard_mongod_tester(
                    self._mdb,
                    shard_index=shard_index,
                    member_index=member_index,
                    username=self._username,
                    password=self._password,
                    use_ssl=self._use_ssl,
                )
                yield (f"shard/{shard_name}/member-{member_index}", tester)

    @property
    def expected_node_count(self) -> int:
        """Number of nodes ``iter_node_testers`` will yield."""
        if self._topology == TOPOLOGY_RS:
            return self._rs_members
        return self._shard_count * self._mongods_per_shard

    # ------------------------------------------------------------------
    # Fan-out runs
    # ------------------------------------------------------------------

    def oneshot_all(self) -> list[NodeAvailabilityResult]:
        """Run one ``oneshot_search`` against every node; aggregate per-node.

        Sequential, not parallel — keeps wire-op streams cleanly
        attributed to a single node and avoids overloading the mongot
        / envoy plane on small dev clusters. A 3-member RS or 2x1 shard
        layout completes in a few seconds.
        """
        results: list[NodeAvailabilityResult] = []
        for node_id, tester in self.iter_node_testers():
            tool = SearchConnectivityTool(tester)
            oneshot = tool.oneshot_search()
            logger.info(
                f"oneshot_all node={node_id}: success={oneshot.success} "
                f"n={oneshot.returned_count} "
                f"latency_ms={oneshot.latency_ms:.1f}"
                + (
                    f" err={oneshot.error_class}({oneshot.error_code}): {oneshot.error_message}"
                    if not oneshot.success
                    else ""
                )
            )
            results.append(
                NodeAvailabilityResult(
                    node_id=node_id,
                    oneshot_result=oneshot,
                )
            )
        return results

    def paging_all(
        self,
        *,
        pages: int = 5,
        interval_seconds: float = 0.1,
        batch_size: int = 10,
    ) -> list[NodeAvailabilityResult]:
        """Run a ``paging_search`` of ``pages`` pages against every node.

        Aggregates each node's per-page results into a
        ``ConnectivityVerdict`` exposed as ``NodeAvailabilityResult.paging_verdict``.
        Full per-page list is kept on ``paging_results`` for callers
        that want to compute a different verdict shape.
        """
        results: list[NodeAvailabilityResult] = []
        for node_id, tester in self.iter_node_testers():
            tool = SearchConnectivityTool(tester)
            page_results = tool.paging_search(
                pages=pages,
                interval_seconds=interval_seconds,
                batch_size=batch_size,
            )
            verdict = tool.verdict(page_results)
            logger.info(f"paging_all node={node_id}: verdict={verdict.as_dict()}")
            results.append(
                NodeAvailabilityResult(
                    node_id=node_id,
                    paging_results=page_results,
                    paging_verdict=verdict,
                )
            )
        return results

    def run(
        self,
        *,
        pages: int = 5,
        interval_seconds: float = 0.1,
        batch_size: int = 10,
    ) -> list[NodeAvailabilityResult]:
        """Run both ``oneshot_search`` and ``paging_search`` against every node.

        Returns one ``NodeAvailabilityResult`` per node with BOTH
        ``oneshot_result`` and ``paging_results`` / ``paging_verdict``
        populated. One SearchConnectivityTool is built per node and
        reused across the two probes for that node, so each per-node
        result captures the full picture under a single CommandListener
        snapshot.
        """
        results: list[NodeAvailabilityResult] = []
        for node_id, tester in self.iter_node_testers():
            tool = SearchConnectivityTool(tester)

            oneshot = tool.oneshot_search()
            logger.info(f"run/oneshot node={node_id}: success={oneshot.success} " f"n={oneshot.returned_count}")

            page_results = tool.paging_search(
                pages=pages,
                interval_seconds=interval_seconds,
                batch_size=batch_size,
            )
            verdict = tool.verdict(page_results)
            logger.info(f"run/paging node={node_id}: verdict={verdict.as_dict()}")

            results.append(
                NodeAvailabilityResult(
                    node_id=node_id,
                    oneshot_result=oneshot,
                    paging_results=page_results,
                    paging_verdict=verdict,
                )
            )
        return results
