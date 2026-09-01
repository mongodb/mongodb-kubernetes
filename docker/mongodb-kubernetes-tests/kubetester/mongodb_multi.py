from __future__ import annotations

import copy
import time
from typing import Dict, List, Optional

from kubernetes import client
from kubetester import decentralized_fanout, wait_until
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoTester, MultiReplicaSetTester
from kubetester.multicluster_client import MultiClusterClient

# Keys that only make sense on the cluster that produced them; stripped when seeding a peer's
# copy of the CR from the primary's backing_obj.
_METADATA_KEYS_TO_STRIP_ON_CREATE = (
    "resourceVersion",
    "uid",
    "creationTimestamp",
    "generation",
    "managedFields",
    "ownerReferences",
)


class MongoDBMulti(MongoDB):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbmulticluster",
            "kind": "MongoDBMultiCluster",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBMulti, self).__init__(*args, **with_defaults)

    # --- Decentralized fan-out --------------------------------------------------------------
    #
    # Legacy tests write the CR through self.api, which is bound to a single (primary) cluster.
    # In decentralized mode every cluster's member-spec fence only trusts its OWN copy of the CR,
    # so create/update/patch/delete additionally replicate to every other cluster in the
    # kubetester.decentralized_fanout registry when it is enabled. Delivery is best-effort-safe
    # by construction (a fence miss self-heals once the leader re-drives), but this helper's
    # contract is stricter: attempt every cluster and report every failure.

    def create(self, dry_run: str = None) -> "MongoDBMulti":
        if not decentralized_fanout.is_enabled():
            return super().create(dry_run=dry_run)

        result = super().create(dry_run=dry_run)
        if not dry_run:
            self._fanout_write("create")
        return result

    def update(self) -> "MongoDBMulti":
        # The base update() dispatches to create_or_update(), which itself calls self.create()
        # or self.patch() — both overridden below, so the fan-out already happens there. Doing it
        # again here would deliver every write to every peer twice.
        return super().update()

    def patch(self):
        if not decentralized_fanout.is_enabled():
            return super().patch()

        result = super().patch()
        self._fanout_write("patch")
        return result

    def delete(self):
        if not decentralized_fanout.is_enabled():
            return super().delete()

        errors: Dict[str, Exception] = {}

        try:
            super().delete()
        except client.ApiException as e:
            if e.status != 404:
                errors[decentralized_fanout.primary_name()] = e

        for cluster_name, api_client in decentralized_fanout.peer_clients().items():
            peer_api = client.CustomObjectsApi(api_client=api_client)
            try:
                peer_api.delete_namespaced_custom_object(self.group, self.version, self.namespace, self.plural, self.name)
            except client.ApiException as e:
                if e.status != 404:
                    errors[cluster_name] = e

        if errors:
            raise _fanout_error("delete", self.name, errors)

        self._wait_for_absence_everywhere()

    def _fanout_write(self, operation: str) -> None:
        errors: Dict[str, Exception] = {}
        for cluster_name, api_client in decentralized_fanout.peer_clients().items():
            try:
                self._replicate_to_peer(client.CustomObjectsApi(api_client=api_client))
            except Exception as e:
                errors[cluster_name] = e

        if errors:
            raise _fanout_error(operation, self.name, errors)

    def _replicate_to_peer(self, api: client.CustomObjectsApi) -> None:
        try:
            peer_obj = api.get_namespaced_custom_object(self.group, self.version, self.namespace, self.plural, self.name)
        except client.ApiException as e:
            if e.status != 404:
                raise
            peer_obj = None

        if peer_obj is None:
            sanitized = copy.deepcopy(self.backing_obj)
            metadata = sanitized.setdefault("metadata", {})
            for key in _METADATA_KEYS_TO_STRIP_ON_CREATE:
                metadata.pop(key, None)
            sanitized.pop("status", None)
            api.create_namespaced_custom_object(
                self.group, self.version, self.namespace, self.plural, sanitized, field_validation="Strict"
            )
            return

        # A merge-patch would leave fields removed from the source (e.g. a scaled-down
        # clusterSpecList entry) untouched on the peer, so this replaces the whole object instead.
        # The peer's own resourceVersion (already on peer_obj) is preserved for the PUT.
        source_metadata = self.backing_obj.get("metadata", {})
        peer_obj["spec"] = copy.deepcopy(self.backing_obj["spec"])
        peer_metadata = peer_obj.setdefault("metadata", {})
        if "annotations" in source_metadata:
            peer_metadata["annotations"] = copy.deepcopy(source_metadata["annotations"])
        if "labels" in source_metadata:
            peer_metadata["labels"] = copy.deepcopy(source_metadata["labels"])

        api.replace_namespaced_custom_object(
            self.group, self.version, self.namespace, self.plural, self.name, peer_obj
        )

    def _wait_for_absence_everywhere(self, timeout: int = 60, interval: int = 3) -> None:
        def still_present() -> List[str]:
            remaining = []
            for cluster_name, api_client in decentralized_fanout.all_clients().items():
                api = client.CustomObjectsApi(api_client=api_client)
                try:
                    api.get_namespaced_custom_object(self.group, self.version, self.namespace, self.plural, self.name)
                    remaining.append(cluster_name)
                except client.ApiException as e:
                    if e.status != 404:
                        raise
            return remaining

        deadline = time.time() + timeout
        remaining = still_present()
        while remaining and time.time() < deadline:
            time.sleep(interval)
            remaining = still_present()

        if remaining:
            raise Exception(
                f"delete of MongoDBMultiCluster {self.name} did not converge: "
                f"still present on cluster(s) {', '.join(sorted(remaining))}"
            )

    def read_statefulsets(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1StatefulSet]:
        statefulsets = {}
        for mcc in clients:
            statefulsets[mcc.cluster_name] = mcc.read_namespaced_stateful_set(
                f"{self.name}-{mcc.cluster_index}", self.namespace
            )
        return statefulsets

    def get_item_spec(self, cluster_name: str) -> Dict:
        for spec in sorted(
            self["spec"]["clusterSpecList"],
            key=lambda x: x["clusterName"],
        ):
            if spec["clusterName"] == cluster_name:
                return spec

        raise ValueError(f"Cluster with name {cluster_name} not found!")

    def read_services(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1Service]:
        services = {}
        for mcc in clients:
            spec = self.get_item_spec(mcc.cluster_name)
            for i, item in enumerate(spec):
                services[mcc.cluster_name] = mcc.read_namespaced_service(
                    f"{self.name}-{mcc.cluster_index}-{i}-svc", self.namespace
                )
        return services

    def read_headless_services(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1Service]:
        services = {}
        for mcc in clients:
            services[mcc.cluster_name] = mcc.read_namespaced_service(
                f"{self.name}-{mcc.cluster_index}-svc", self.namespace
            )
        return services

    def read_configmaps(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1ConfigMap]:
        configmaps = {}
        for mcc in clients:
            configmaps[mcc.cluster_name] = mcc.read_namespaced_config_map(
                f"{self.name}-hostname-override", self.namespace
            )
        return configmaps

    def service_names(self) -> List[str]:
        # TODO: this function does not account for previous
        # clusters being removed, the indices do not line up
        # and as a result the incorrect service name will be returned.
        service_names = []
        cluster_specs = sorted(
            self["spec"]["clusterSpecList"],
            key=lambda x: x["clusterName"],
        )
        for i, spec in enumerate(cluster_specs):
            for j in range(spec["members"]):
                service_names.append(f"{self.name}-{i}-{j}-svc")
        return service_names

    def assert_statefulsets_are_ready(self, clients: List[MultiClusterClient], timeout: int = 600):
        def fn():
            statefulsets = self.read_statefulsets(clients)

            assert len(statefulsets) == len(self["spec"]["clusterSpecList"])

            for i, mcc in enumerate(clients):
                cluster_sts = statefulsets[mcc.cluster_name]
                if cluster_sts.status.ready_replicas != self.get_item_spec(mcc.cluster_name)["members"]:
                    return False

            return True

        wait_until(fn, timeout=timeout, interval=10, message="Waiting for all statefulsets to be ready")

    def tester(
        self,
        ca_path: Optional[str] = None,
        srv: bool = False,
        use_ssl: Optional[bool] = None,
        service_names: Optional[List[str]] = None,
        port="27017",
        external: bool = False,
    ) -> MongoTester:
        if service_names is None:
            service_names = self.service_names()

        return MultiReplicaSetTester(
            service_names=service_names,
            namespace=self.namespace,
            port=port,
            external=external,
            ssl=self.is_tls_enabled() if use_ssl is None else use_ssl,
            ca_path=ca_path,
        )


def _fanout_error(operation: str, name: str, errors: Dict[str, Exception]) -> Exception:
    details = "; ".join(f"{cluster}: {error}" for cluster, error in sorted(errors.items()))
    return Exception(f"decentralized fan-out {operation} failed for MongoDBMultiCluster {name} on cluster(s): {details}")
