from __future__ import annotations

import time
from typing import Optional

from kubeobject import CustomObject
from kubetester.mongodb import MongoDB
from kubetester.mongodb_utils_state import in_desired_state
from kubetester.phase import Phase
from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")


class MongoDBSearch(MongoDB, CustomObject):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbsearch",
            "kind": "MongoDBSearch",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBSearch, self).__init__(*args, **with_defaults)

    @classmethod
    def from_yaml(cls, yaml_file, name=None, namespace=None, with_mdb_version_from_env=False) -> MongoDBSearch:
        resource = super().from_yaml(yaml_file=yaml_file, name=name, namespace=namespace)
        return resource

    @TRACER.start_as_current_span("assert_reaches_phase")
    def assert_reaches_phase(self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False):
        start_time = time.time()

        self.wait_for(
            lambda s: in_desired_state(
                current_state=self.get_status_phase(),
                desired_state=phase,
                current_generation=self.get_generation(),
                observed_generation=self.get_status_observed_generation(),
                current_message=self.get_status_message(),
                msg_regexp=msg_regexp,
                ignore_errors=ignore_errors,
            ),
            timeout,
            should_raise=True,
        )

        end_time = time.time()
        span = trace.get_current_span()
        span.set_attribute("mck.resource", self.__class__.__name__)
        span.set_attribute("mck.action", "assert_phase")
        span.set_attribute("mck.desired_phase", phase.name)
        span.set_attribute("mck.time_needed", end_time - start_time)
        logger.debug(
            f"Reaching phase {phase.name} for resource {self.__class__.__name__} took {end_time - start_time}s"
        )

    def get_lb_status(self) -> Optional[dict]:
        """Returns the status.loadBalancer substatus dict, or None if absent."""
        try:
            return self["status"]["loadBalancer"]
        except KeyError:
            return None

    def get_lb_status_phase(self) -> Optional[Phase]:
        """Returns the loadBalancer substatus phase, or None if absent."""
        lb = self.get_lb_status()
        if lb is None:
            return None
        try:
            return Phase[lb["phase"]]
        except KeyError:
            return None

    def is_lb_mode_managed(self) -> bool:
        """Returns True if any cluster entry has spec.clusters[].loadBalancer.managed set."""
        try:
            return any("managed" in (c.get("loadBalancer") or {}) for c in self["spec"]["clusters"])
        except KeyError:
            return False

    def assert_lb_status(self):
        """Asserts the loadBalancer substatus is consistent with the LB mode.

        - Managed: status.loadBalancer must exist with phase Running.
        - Unmanaged / no LB: status.loadBalancer must be absent.
        """
        self.load()
        lb = self.get_lb_status()

        if self.is_lb_mode_managed():
            assert lb is not None, "status.loadBalancer is missing for managed LB"
            lb_phase = self.get_lb_status_phase()
            assert lb_phase == Phase.Running, f"status.loadBalancer.phase is {lb_phase}, expected Running"
            logger.info(f"MongoDBSearch {self.name}: loadBalancer status is Running")
        else:
            assert lb is None, f"status.loadBalancer should be absent for non-managed LB, got: {lb}"
            logger.info(f"MongoDBSearch {self.name}: loadBalancer status correctly absent")

    def get_cluster_statuses(self) -> list[dict]:
        """Returns the status.clusters list, or [] if absent.

        The search controller is the sole writer and recomputes the whole list
        every reconcile (one entry per spec.clusters[], keyed by index).
        """
        try:
            return list(self["status"]["clusters"] or [])
        except KeyError:
            return []

    def get_cluster_status(self, cluster_index: int) -> Optional[dict]:
        """Returns the per-cluster status entry with the given index, or None."""
        for cs in self.get_cluster_statuses():
            if cs.get("index") == cluster_index:
                return cs
        return None

    def _spec_cluster_indexes(self) -> list[int]:
        """The pinned spec.clusters[].index values, defaulting to positional index."""
        clusters = self["spec"].get("clusters", []) or []
        return [c.get("index", i) for i, c in enumerate(clusters)]

    def assert_cluster_statuses(
        self,
        expected_count: Optional[int] = None,
        expect_managed_lb: Optional[bool] = None,
        expect_metrics_forwarder: bool = False,
    ):
        """Assert status.clusters is well-formed for a healthy (Running) deployment.

        - One entry per spec.clusters[], no more, no less (expected_count overrides).
        - index values exactly match the spec pins and are unique.
        - Every entry's search phase is Running with an empty searchMessage.
        - loadBalancer phase is Running (empty message) iff managed LB, else absent/empty
          (expect_managed_lb overrides is_lb_mode_managed()).
        - metricsForwarder phase is Running (empty message) iff expect_metrics_forwarder,
          else absent/empty.
        """
        self.load()
        statuses = self.get_cluster_statuses()
        spec_indexes = self._spec_cluster_indexes()

        want_count = expected_count if expected_count is not None else len(spec_indexes)
        assert len(statuses) == want_count, f"expected {want_count} clusters entries, got {len(statuses)}: {statuses}"

        got_indexes = [cs.get("index") for cs in statuses]
        assert len(set(got_indexes)) == len(got_indexes), f"duplicate index in status.clusters: {got_indexes}"
        if expected_count is None:
            assert sorted(got_indexes) == sorted(
                spec_indexes
            ), f"status.clusters indexes {sorted(got_indexes)} != spec.clusters indexes {sorted(spec_indexes)}"

        managed_lb = expect_managed_lb if expect_managed_lb is not None else self.is_lb_mode_managed()

        for cs in statuses:
            ci = cs.get("index")
            assert (
                cs.get("search") == "Running"
            ), f"cluster {ci}: search phase is {cs.get('search')!r}, expected Running"
            assert not cs.get(
                "searchMessage"
            ), f"cluster {ci}: searchMessage should be empty when Running, got {cs.get('searchMessage')!r}"
            if managed_lb:
                assert (
                    cs.get("loadBalancer") == "Running"
                ), f"cluster {ci}: loadBalancer phase is {cs.get('loadBalancer')!r}, expected Running"
                assert not cs.get("loadBalancerMessage"), (
                    f"cluster {ci}: loadBalancerMessage should be empty when Running, "
                    f"got {cs.get('loadBalancerMessage')!r}"
                )
            else:
                assert not cs.get(
                    "loadBalancer"
                ), f"cluster {ci}: loadBalancer should be absent for non-managed LB, got {cs.get('loadBalancer')!r}"

            if expect_metrics_forwarder:
                assert (
                    cs.get("metricsForwarder") == "Running"
                ), f"cluster {ci}: metricsForwarder phase is {cs.get('metricsForwarder')!r}, expected Running"
                assert not cs.get("metricsForwarderMessage"), (
                    f"cluster {ci}: metricsForwarderMessage should be empty when Running, "
                    f"got {cs.get('metricsForwarderMessage')!r}"
                )
            else:
                assert not cs.get("metricsForwarder"), (
                    f"cluster {ci}: metricsForwarder should be absent when forwarder disabled, "
                    f"got {cs.get('metricsForwarder')!r}"
                )

        logger.info(
            f"MongoDBSearch {self.name}: status.clusters OK ({len(statuses)} entries, "
            f"managed_lb={managed_lb}, metrics_forwarder={expect_metrics_forwarder})"
        )

    def wait_for_cluster_search_phase(
        self,
        cluster_index: int,
        expected_phase: Phase,
        expect_message: bool,
        timeout: int = 300,
    ):
        """Poll until the given cluster's per-cluster SEARCH phase reaches expected_phase.

        When expect_message is True, also require a non-empty searchMessage when False, require an empty searchMessage.
        """
        self._wait_for_cluster_phase("search", "searchMessage", cluster_index, expected_phase, expect_message, timeout)

    def wait_for_cluster_lb_phase(
        self,
        cluster_index: int,
        expected_phase: Phase,
        expect_message: bool,
        timeout: int = 300,
    ):
        """Poll until the given cluster's per-cluster LOAD BALANCER phase reaches expected_phase.
        """
        self._wait_for_cluster_phase(
            "loadBalancer", "loadBalancerMessage", cluster_index, expected_phase, expect_message, timeout
        )

    def wait_for_cluster_metrics_forwarder_phase(
        self,
        cluster_index: int,
        expected_phase: Phase,
        expect_message: bool,
        timeout: int = 300,
    ):
        """Poll until the given cluster's per-cluster METRICS FORWARDER phase reaches expected_phase.
        """
        self._wait_for_cluster_phase(
            "metricsForwarder", "metricsForwarderMessage", cluster_index, expected_phase, expect_message, timeout
        )

    def _wait_for_cluster_phase(
        self,
        phase_key: str,
        message_key: str,
        cluster_index: int,
        expected_phase: Phase,
        expect_message: bool,
        timeout: int,
    ):
        from kubetester.kubetester import run_periodically

        def check() -> tuple:
            self.load()
            cs = self.get_cluster_status(cluster_index)
            if cs is None:
                return (
                    False,
                    f"no status.clusters entry for index {cluster_index}: {self.get_cluster_statuses()}",
                )
            phase = cs.get(phase_key)
            msg = cs.get(message_key) or ""
            if phase != expected_phase.name:
                return (
                    False,
                    f"cluster {cluster_index}: {phase_key}={phase!r}, want {expected_phase.name!r} (msg={msg!r})",
                )
            if expect_message and not msg:
                return False, f"cluster {cluster_index}: {phase_key}={phase!r} but {message_key} is empty"
            if not expect_message and msg:
                return False, f"cluster {cluster_index}: {phase_key}={phase!r} but {message_key} not cleared: {msg!r}"
            return True, f"cluster {cluster_index}: {phase_key}={phase} (msg={msg!r}) as expected"

        run_periodically(
            check,
            timeout=timeout,
            sleep_time=10,
            msg=f"cluster {cluster_index} {phase_key} -> {expected_phase.name}",
        )

    def get_metrics_forwarder_status(self) -> Optional[dict]:
        """Returns the status.metricsForwarder substatus dict, or None if absent."""
        try:
            return self["status"]["metricsForwarder"]
        except KeyError:
            return None

    def get_metrics_forwarder_status_phase(self) -> Optional[Phase]:
        """Returns the metricsForwarder substatus phase, or None if absent."""
        mf = self.get_metrics_forwarder_status()
        if mf is None:
            return None
        try:
            return Phase[mf["phase"]]
        except KeyError:
            return None

    def mongot_pod_hostnames(self, cluster_index: int = 0) -> set:
        """Return the set of FQDN hostnames for all mongot pods in a replica-set deployment.

        Mirrors the Go naming:
          StatefulSetNamespacedNameForCluster(cluster_index) / SearchServiceNamespacedNameForCluster(cluster_index)

        Each pod is addressed as:
          {name}-search-{cluster_index}-{i}.{name}-search-{cluster_index}-svc.{namespace}.svc.cluster.local
        """
        replicas = self["spec"]["clusters"][cluster_index].get("replicas", 1)
        sts = f"{self.name}-search-{cluster_index}"
        svc = f"{self.name}-search-{cluster_index}-svc"
        return {f"{sts}-{i}.{svc}.{self.namespace}.svc.cluster.local" for i in range(replicas)}

    def shard_mongot_pod_hostnames(self, shard_names: list, cluster_index: int = 0) -> set:
        """Return the set of FQDN hostnames for all mongot pods across every shard.

        Mirrors the Go naming:
          MongotStatefulSetForClusterShard(cluster_index, shard_name) / MongotServiceForClusterShard(cluster_index, shard_name)

        Each pod is addressed as:
          {name}-search-{cluster_index}-{shard_name}-{i}.{name}-search-{cluster_index}-{shard_name}-svc.{namespace}.svc.cluster.local

        Per-shard replica counts from spec.clusters[cluster_index].shardOverrides[].replicas take
        precedence over the cluster-level replicas for the named shards (mirrors Go's findShardOverride).
        """
        cluster = self["spec"]["clusters"][cluster_index]
        cluster_replicas = cluster.get("replicas", 1)
        shard_overrides = cluster.get("shardOverrides", [])

        def replicas_for_shard(shard_name: str) -> int:
            for override in shard_overrides:
                if shard_name in override.get("shardNames", []) and override.get("replicas") is not None:
                    return override["replicas"]
            return cluster_replicas

        hostnames = set()
        for shard_name in shard_names:
            sts = f"{self.name}-search-{cluster_index}-{shard_name}"
            svc = f"{self.name}-search-{cluster_index}-{shard_name}-svc"
            for i in range(replicas_for_shard(shard_name)):
                hostnames.add(f"{sts}-{i}.{svc}.{self.namespace}.svc.cluster.local")
        return hostnames
