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
