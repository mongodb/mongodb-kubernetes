import time
from contextlib import ExitStack
from copy import deepcopy
from typing import Callable, Mapping, Optional
from uuid import uuid4

import kubernetes
import pymongo.errors
from kubernetes.client import CoreV1Api
from kubernetes.utils import parse_quantity
from kubetester import create_or_update_secret, pod_is_ready, try_load
from kubetester.certs import create_mongodb_tls_certs, create_sharded_cluster_certs
from kubetester.kubetester import KubernetesTester, ensure_nested_objects
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.connectivity import SEARCH_OWNER_NAME_LABEL, wait_for_resource_deleted
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

_ISSUER_BY_NAMESPACE: dict[str, str] = {}

RESIZE_STORAGE_CLASS = "csi-hostpath-sc"
RESIZE_INITIAL_STORAGE = "1Gi"
RESIZE_TARGET_STORAGE = "2Gi"


class SearchPVCResize:
    """One mongot StatefulSet (replica set, or one shard when shard_name is set) in one member cluster.

    storage returns the mapping in the MongoDBSearch spec whose "storage" key sizes this StatefulSet;
    it defaults to the cluster entry's persistence.
    """

    def __init__(
        self,
        search: MongoDBSearch,
        cluster_index: int,
        api_client: Optional[kubernetes.client.ApiClient],
        movies: SampleMoviesSearchHelper,
        shard_name: Optional[str] = None,
        storage: Optional[Callable[[dict], dict]] = None,
    ):
        self.search = search
        self.cluster_index = cluster_index
        self.api_client = api_client
        self.core = kubernetes.client.CoreV1Api(api_client)
        self.apps = kubernetes.client.AppsV1Api(api_client)
        self.namespace = search.namespace
        if shard_name is None:
            self.name = search_resource_names.mongot_statefulset_name_for_cluster(search.name, cluster_index)
        else:
            self.name = search_resource_names.shard_statefulset_name(search.name, shard_name, cluster_index)

        def cluster_persistence(spec: dict) -> dict:
            entry = next(c for c in spec["clusters"] if c.get("index", 0) == cluster_index)
            return entry["persistence"]["single"]

        self.storage = storage or cluster_persistence
        self.movies = movies
        self.marker_path = "/mongot/data/pvc-resize-marker"
        self.pods: dict[str, kubernetes.client.V1Pod] = {}
        self.claims: dict[str, kubernetes.client.V1PersistentVolumeClaim] = {}
        self.pod_claims: dict[str, str] = {}

    def evidence(self, message: str) -> None:
        logger.info(f"PVC-RESIZE-EVIDENCE: index={self.cluster_index} sts={self.name} {message}")

    def read_sts(self):
        return self.apps.read_namespaced_stateful_set(self.name, self.namespace)

    def exec(self, pod_name: str, command: list[str]) -> str:
        return KubernetesTester.run_command_in_pod_container(
            pod_name, self.namespace, command, container="mongot", api_client=self.api_client
        )

    @staticmethod
    def template_storage(sts) -> str:
        templates = [claim for claim in sts.spec.volume_claim_templates if claim.metadata.name == "data"]
        assert len(templates) == 1
        return templates[0].spec.resources.requests["storage"]

    def record(self) -> None:
        self.search.assert_reaches_phase(Phase.Running, timeout=600)
        self.movies.assert_search_query(retry_timeout=300)
        self.hits = list(self.movies.execute_example_search_query())
        self.sts = self.read_sts()
        assert parse_quantity(self.template_storage(self.sts)) == parse_quantity(RESIZE_INITIAL_STORAGE)
        assert self.sts.spec.replicas > 0
        assert self.sts.status.ready_replicas == self.sts.spec.replicas
        self.evidence(
            f"baseline stsUID={self.sts.metadata.uid} replicas={self.sts.spec.replicas} "
            f"retention={self.sts.spec.persistent_volume_claim_retention_policy}"
        )
        selector = ",".join(f"{k}={v}" for k, v in self.sts.spec.selector.match_labels.items())
        pods = self.core.list_namespaced_pod(self.namespace, label_selector=selector).items
        assert len(pods) == self.sts.spec.replicas
        for pod in pods:
            assert pod_is_ready(pod) and pod.metadata.deletion_timestamp is None
            assert any(ref.uid == self.sts.metadata.uid for ref in pod.metadata.owner_references or [])
            volumes = [volume for volume in pod.spec.volumes if volume.name == "data"]
            assert len(volumes) == 1 and volumes[0].persistent_volume_claim is not None
            claim_name = volumes[0].persistent_volume_claim.claim_name
            claim = self.core.read_namespaced_persistent_volume_claim(claim_name, self.namespace)
            assert claim_name not in self.claims
            self.pods[pod.metadata.name] = pod
            self.claims[claim_name] = claim
            self.pod_claims[pod.metadata.name] = claim_name
            assert parse_quantity(claim.spec.resources.requests["storage"]) == parse_quantity(RESIZE_INITIAL_STORAGE)
            assert parse_quantity(claim.status.capacity["storage"]) >= parse_quantity(RESIZE_INITIAL_STORAGE)
            storage_class = kubernetes.client.StorageV1Api(self.api_client).read_storage_class(
                claim.spec.storage_class_name
            )
            assert storage_class.allow_volume_expansion is True
            self.exec(pod.metadata.name, ["sh", "-c", f"printf %s {pod.metadata.uid} > {self.marker_path}"])
            self.assert_marker(pod.metadata.name)
        assert set(self.claims) == {f"data-{self.name}-{ordinal}" for ordinal in range(self.sts.spec.replicas)}
        self.assert_claims()

    def assert_claims(self, expanded: bool = False) -> None:
        for name, original in self.claims.items():
            claim = self.core.read_namespaced_persistent_volume_claim(name, self.namespace)
            assert claim.metadata.uid == original.metadata.uid
            assert claim.spec.volume_name == original.spec.volume_name
            assert claim.status.phase == "Bound"
            assert claim.metadata.deletion_timestamp is None
            if expanded:
                assert parse_quantity(claim.spec.resources.requests["storage"]) >= parse_quantity(RESIZE_TARGET_STORAGE)
                assert parse_quantity(claim.status.capacity["storage"]) >= parse_quantity(RESIZE_TARGET_STORAGE)
            self.evidence(
                f"claim={name} uid={claim.metadata.uid} pv={claim.spec.volume_name} "
                f"request={claim.spec.resources.requests['storage']} capacity={claim.status.capacity} "
                f"conditions={claim.status.conditions} owners={claim.metadata.owner_references}"
            )

    def assert_marker(self, name: str) -> None:
        assert self.exec(name, ["cat", self.marker_path]).strip() == self.pods[name].metadata.uid

    def assert_surviving_pods(self) -> None:
        for name, original in self.pods.items():
            pod = self.core.read_namespaced_pod(name, self.namespace)
            assert pod.metadata.uid == original.metadata.uid
            assert pod.metadata.deletion_timestamp is None
            original_counts = {s.name: s.restart_count for s in original.status.container_statuses}
            deltas = {s.name: s.restart_count - original_counts[s.name] for s in pod.status.container_statuses}
            self.evidence(f"pod={name} uid={pod.metadata.uid} restartDeltas={deltas}")
            assert all(delta == 0 for delta in deltas.values())
            self.assert_marker(name)

    def patch_claims(self) -> None:
        for name in self.claims:
            self.core.patch_namespaced_persistent_volume_claim(
                name, self.namespace, {"spec": {"resources": {"requests": {"storage": RESIZE_TARGET_STORAGE}}}}
            )

    def controller_expanded(self) -> tuple[bool, str]:
        pending = []
        for name in self.claims:
            claim = self.core.read_namespaced_persistent_volume_claim(name, self.namespace)
            pv = self.core.read_persistent_volume(claim.spec.volume_name)
            capacity = parse_quantity((claim.status.capacity or {}).get("storage", "0"))
            fs_pending = any(
                c.type == "FileSystemResizePending" and c.status == "True" for c in claim.status.conditions or []
            )
            if parse_quantity(pv.spec.capacity["storage"]) < parse_quantity(RESIZE_TARGET_STORAGE) or (
                capacity < parse_quantity(RESIZE_TARGET_STORAGE) and not fs_pending
            ):
                pending.append(name)
        return not pending, f"controller expansion pending: {pending}"

    def assert_absent(self) -> None:
        try:
            self.read_sts()
        except kubernetes.client.exceptions.ApiException as exc:
            if exc.status != 404:
                raise
        else:
            raise AssertionError(f"{self.name} recreated while operator stopped")
        self.assert_claims()
        self.assert_surviving_pods()

    def wait_for_adoption(self) -> None:
        def adopted():
            try:
                sts = self.read_sts()
            except kubernetes.client.exceptions.ApiException as exc:
                if exc.status != 404:
                    raise
                return False
            if sts.metadata.uid == self.sts.metadata.uid:
                return False
            if parse_quantity(self.template_storage(sts)) != parse_quantity(RESIZE_TARGET_STORAGE):
                return False
            assert sts.spec.replicas == self.sts.spec.replicas
            for name in self.pods:
                pod = self.core.read_namespaced_pod(name, self.namespace)
                if not any(ref.uid == sts.metadata.uid for ref in pod.metadata.owner_references or []):
                    return False
            for name in self.claims:
                claim = self.core.read_namespaced_persistent_volume_claim(name, self.namespace)
                if not any(ref.uid == sts.metadata.uid for ref in claim.metadata.owner_references or []):
                    return False
            return True

        run_periodically(adopted, timeout=600, sleep_time=5, msg=f"{self.name} recreation and pod/PVC adoption")
        self.evidence(f"recreated stsUID={self.read_sts().metadata.uid}")
        self.assert_claims()
        self.assert_surviving_pods()

    def restart_serially(self, filesystem_only: bool) -> None:
        for name in self.pods:
            claim = self.core.read_namespaced_persistent_volume_claim(self.pod_claims[name], self.namespace)
            if filesystem_only and not any(
                c.type == "FileSystemResizePending" and c.status == "True" for c in claim.status.conditions or []
            ):
                continue
            old = self.core.read_namespaced_pod(name, self.namespace)
            self.evidence(f"restart pod={name} uid={old.metadata.uid} filesystemOnly={filesystem_only}")
            self.core.delete_namespaced_pod(
                name,
                self.namespace,
                body=kubernetes.client.V1DeleteOptions(
                    preconditions=kubernetes.client.V1Preconditions(uid=old.metadata.uid)
                ),
            )

            def replaced():
                try:
                    pod = self.core.read_namespaced_pod(name, self.namespace)
                except kubernetes.client.exceptions.ApiException as exc:
                    if exc.status != 404:
                        raise
                    return False
                return pod.metadata.uid != old.metadata.uid and pod_is_ready(pod)

            run_periodically(replaced, timeout=600, sleep_time=5, msg=f"{name} replacement Ready")
            self.assert_marker(name)
            self.assert_search()

    def wait_for_capacity(self) -> None:
        def complete():
            for name in self.claims:
                claim = self.core.read_namespaced_persistent_volume_claim(name, self.namespace)
                if parse_quantity((claim.status.capacity or {}).get("storage", "0")) < parse_quantity(
                    RESIZE_TARGET_STORAGE
                ):
                    return False
                if any(
                    c.type == "FileSystemResizePending" and c.status == "True" for c in claim.status.conditions or []
                ):
                    return False
            return True

        run_periodically(complete, timeout=600, sleep_time=5, msg=f"{self.name} filesystem expansion complete")
        self.assert_claims(expanded=True)

    def assert_search(self) -> None:
        self.movies.assert_search_query(retry_timeout=300)
        assert sorted(self.movies.execute_example_search_query(), key=lambda row: row["title"]) == sorted(
            self.hits, key=lambda row: row["title"]
        )


def run_search_pvc_resize(
    units: list[SearchPVCResize],
    operators: list[tuple[Optional[kubernetes.client.ApiClient], str]],
    writer: SampleMoviesSearchHelper,
) -> None:
    assert units and operators
    namespace = units[0].namespace

    def assert_label_lookup():
        by_cluster: dict[int, list[SearchPVCResize]] = {}
        for unit in units:
            by_cluster.setdefault(id(unit.api_client), []).append(unit)
        for group in by_cluster.values():
            selector = f"{SEARCH_OWNER_NAME_LABEL}={group[0].search.name}"
            found = group[0].apps.list_namespaced_stateful_set(namespace, label_selector=selector).items
            assert sorted(sts.metadata.name for sts in found) == sorted(unit.name for unit in group)

    for unit in units:
        unit.record()
    assert_label_lookup()
    deployments = []
    for api_client, name in operators:
        apps = kubernetes.client.AppsV1Api(api_client)
        core = kubernetes.client.CoreV1Api(api_client)
        deployment = apps.read_namespaced_deployment(name, namespace)
        assert deployment.spec.replicas > 0
        assert not deployment.spec.selector.match_expressions
        selector = ",".join(f"{k}={v}" for k, v in deployment.spec.selector.match_labels.items())
        deployments.append((apps, core, deployment, selector))
        logger.info(
            f"PVC-RESIZE-EVIDENCE: operator={name} uid={deployment.metadata.uid} replicas={deployment.spec.replicas}"
        )
    for unit in units:
        unit.patch_claims()
    for unit in units:
        run_periodically(unit.controller_expanded, timeout=900, sleep_time=5, msg=f"{unit.name} controller expansion")
        unit.assert_claims()

    def restore(apps, deployment):
        name = deployment.metadata.name
        apps.patch_namespaced_deployment_scale(name, namespace, {"spec": {"replicas": deployment.spec.replicas}})

        def ready():
            current = apps.read_namespaced_deployment(name, namespace)
            return (
                current.status.observed_generation >= current.metadata.generation
                and current.status.ready_replicas == deployment.spec.replicas
                and current.status.updated_replicas == deployment.spec.replicas
            )

        run_periodically(ready, timeout=600, sleep_time=5, msg=f"operator {name} restored to original replicas")

    # ExitStack attempts every restore even if a previous restore or the procedure fails.
    with ExitStack() as cleanup:
        for apps, core, deployment, selector in deployments:
            cleanup.callback(restore, apps, deployment)
            apps.patch_namespaced_deployment_scale(deployment.metadata.name, namespace, {"spec": {"replicas": 0}})
        for apps, core, deployment, selector in deployments:

            def stopped():
                current = apps.read_namespaced_deployment(deployment.metadata.name, namespace)
                return (
                    current.spec.replicas == 0
                    and not core.list_namespaced_pod(namespace, label_selector=selector).items
                )

            run_periodically(
                stopped,
                timeout=300,
                sleep_time=5,
                msg=f"all operator {deployment.metadata.name} pods stopped",
            )
        for unit in units:
            unit.apps.delete_namespaced_stateful_set(
                unit.name,
                namespace,
                body=kubernetes.client.V1DeleteOptions(
                    propagation_policy="Orphan",
                    preconditions=kubernetes.client.V1Preconditions(uid=unit.sts.metadata.uid),
                ),
            )
            wait_for_resource_deleted(unit.read_sts, unit.name)
            unit.assert_absent()
        # Every copy gets every unit's edit: with an operator per member, each local copy stays consistent.
        for search in {id(unit.search): unit.search for unit in units}.values():
            search.load()
            assert search["metadata"].get("deletionTimestamp") is None
            original_uid = search["metadata"]["uid"]
            spec = deepcopy(search["spec"])
            for unit in units:
                unit.storage(spec)["storage"] = RESIZE_TARGET_STORAGE
            search["spec"]["clusters"] = spec["clusters"]
            search.update()
            search.load()
            assert search["metadata"]["uid"] == original_uid
            assert search["spec"]["clusters"] == spec["clusters"]
        time.sleep(10)
        for unit in units:
            unit.assert_absent()
    for unit in units:
        unit.wait_for_adoption()
    assert_label_lookup()
    for unit in units:
        unit.restart_serially(filesystem_only=True)
        unit.wait_for_capacity()
    for unit in units:
        unit.search.assert_reaches_phase(Phase.Running, timeout=600)
        unit.assert_search()
    # Test-only rollout also proves persistence when the CSI driver completed online.
    for unit in units:
        unit.restart_serially(filesystem_only=False)
        unit.wait_for_capacity()
        unit.search.assert_reaches_phase(Phase.Running, timeout=600)

    token = f"pvcresize{uuid4().hex}"
    collection = writer.search_tester.client[writer.db_name][writer.col_name]
    collection.insert_one({"_id": token, "title": token, "plot": "Post-resize ingestion probe"})
    for unit in units:

        def ingested():
            try:
                rows = unit.movies.text_search_movies(token)
            except pymongo.errors.PyMongoError as exc:
                return False, str(exc)
            return any(row["title"] == token for row in rows), f"new document {token}: {rows}"

        run_periodically(ingested, timeout=300, sleep_time=5, msg=f"{unit.name} continued ingestion")
        unit.assert_search()
        unit.evidence(f"completed: retained baseline and indexed new document {token}")


def ensure_search_issuer(namespace: str) -> str:
    """Install cert-manager (idempotent) and create the namespace ``ca-issuer``.

    Explicit replacement for the ``issuer`` / ``multi_cluster_issuer`` fixtures —
    both just create the same per-namespace CA Issuer (named ``ca-issuer``) once
    cert-manager is up. Memoized per namespace so repeated hook calls don't re-run
    the helm install + readiness wait. Returns the Issuer name.
    """
    if namespace not in _ISSUER_BY_NAMESPACE:
        from tests.conftest import (
            create_issuer,
            get_central_cluster_client,
            get_central_cluster_name,
            install_cert_manager,
            wait_for_cert_manager_ready,
        )

        central_client = get_central_cluster_client()
        install_cert_manager(cluster_client=central_client, cluster_name=get_central_cluster_name())
        wait_for_cert_manager_ready(cluster_client=central_client)
        _ISSUER_BY_NAMESPACE[namespace] = create_issuer(namespace=namespace, api_client=central_client)
    return _ISSUER_BY_NAMESPACE[namespace]


class SearchDeploymentHelper:

    def __init__(
        self,
        namespace: str,
        mdb_resource_name: str,
        mdbs_resource_name: str,
        shard_count: int = 2,
        mongods_per_shard: int = 1,
        mongos_count: int = 1,
        config_server_count: int = 1,
        tls_cert_prefix: str = "certs",
        ca_configmap_name: Optional[str] = None,
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ):
        self.namespace = namespace
        self.mdb_resource_name = mdb_resource_name
        self.mdbs_resource_name = mdbs_resource_name
        self.shard_count = shard_count
        self.mongods_per_shard = mongods_per_shard
        self.mongos_count = mongos_count
        self.config_server_count = config_server_count
        self.tls_cert_prefix = tls_cert_prefix
        self.ca_configmap_name = ca_configmap_name or f"{mdb_resource_name}-ca"
        # Cluster the MongoDBUser CRs + their password secrets land in. SC leaves
        # this None (operator-cluster default); MC passes the central-cluster client.
        self.api_client = api_client

    # create_sharded_mdb returns the MongoDB sharded deployment resource, after setting the mongotHost
    # and shardOverrides based on the the function `mongot_host_fn`. For unmanaged loadbalancers it should return the proxy svc's endpoint.
    def create_sharded_mdb(
        self,
        mongot_host_fn: Optional[Callable[[str], str]] = None,
        set_tls_ca: bool = False,
    ) -> MongoDB:
        resource = MongoDB.from_yaml(
            yaml_fixture("enterprise-sharded-cluster-sample-mflix.yaml"),
            name=self.mdb_resource_name,
            namespace=self.namespace,
        )

        if try_load(resource):
            return resource

        resource.configure(om=get_ops_manager(self.namespace), project_name=self.mdb_resource_name)

        if set_tls_ca:
            resource["spec"]["security"]["tls"]["ca"] = self.ca_configmap_name

        if mongot_host_fn is not None:
            shard_overrides = []
            for shard_idx in range(self.shard_count):
                shard_name = f"{self.mdb_resource_name}-{shard_idx}"
                host = mongot_host_fn(shard_name)
                shard_overrides.append(
                    {
                        "shardNames": [shard_name],
                        "additionalMongodConfig": {
                            "setParameter": {
                                "mongotHost": host,
                                "searchIndexManagementHostAndPort": host,
                                "skipAuthenticationToSearchIndexManagementServer": False,
                                "skipAuthenticationToMongot": False,
                                "searchTLSMode": "requireTLS",
                                "useGrpcForSearch": True,
                            }
                        },
                    }
                )
            resource["spec"]["shardOverrides"] = shard_overrides

            first_shard = f"{self.mdb_resource_name}-0"
            mongos_host = mongot_host_fn(first_shard)
            if "mongos" not in resource["spec"]:
                resource["spec"]["mongos"] = {}
            resource["spec"]["mongos"]["additionalMongodConfig"] = {
                "setParameter": {
                    "mongotHost": mongos_host,
                    "searchIndexManagementHostAndPort": mongos_host,
                    "skipAuthenticationToSearchIndexManagementServer": False,
                    "skipAuthenticationToMongot": False,
                    "searchTLSMode": "requireTLS",
                    "useGrpcForSearch": True,
                }
            }

        return resource

    def mdbs_for_ext_sharded_source(
        self,
        mongot_user_name: str,
        lb_endpoint: Optional[str] = None,
        lb_mode: Optional[str] = None,
        replicas: Optional[int] = None,
        shard_overrides: Optional[list] = None,
    ) -> MongoDBSearch:
        resource = MongoDBSearch.from_yaml(
            yaml_fixture("search-sharded-external-mongod.yaml"),
            namespace=self.namespace,
            name=self.mdbs_resource_name,
        )

        if try_load(resource):
            return resource

        router_hosts = [
            f"{self.mdb_resource_name}-mongos-{i}.{self.mdb_resource_name}-svc.{self.namespace}.svc.cluster.local:27017"
            for i in range(self.mongos_count)
        ]

        shards = []
        for shard_idx in range(self.shard_count):
            shard_name = f"{self.mdb_resource_name}-{shard_idx}"
            shard_hosts = [
                f"{shard_name}-{m}.{self.mdb_resource_name}-sh.{self.namespace}.svc.cluster.local:27017"
                for m in range(self.mongods_per_shard)
            ]
            shards.append({"shardName": shard_name, "hosts": shard_hosts})

        resource["spec"]["source"] = {
            "username": mongot_user_name,
            "passwordSecretRef": {
                "name": f"{self.mdbs_resource_name}-{mongot_user_name}-password",
                "key": "password",
            },
            "external": {
                "shardedCluster": {
                    "router": {"hosts": router_hosts},
                    "shards": shards,
                },
                "tls": {"ca": {"name": self.ca_configmap_name}},
            },
        }

        if replicas is not None:
            resource["spec"]["clusters"] = [{"replicas": replicas}]

        if shard_overrides is not None:
            clusters = resource["spec"].get("clusters") or [{}]
            clusters[0]["shardOverrides"] = shard_overrides
            resource["spec"]["clusters"] = clusters

        if lb_mode or lb_endpoint:
            clusters = resource["spec"].get("clusters") or [{}]
            for i, cluster in enumerate(clusters):
                lb = {}
                if lb_mode == "Managed":
                    lb["managed"] = {
                        "externalHostname": search_resource_names.shard_proxy_svc_hostname_template(
                            self.mdbs_resource_name, self.namespace, i
                        ),
                        # Shard-agnostic cluster-level endpoint for mongos: the per-cluster proxy-svc
                        # FQDN (matches the LB cert SAN). Required for external sharded + managed LB.
                        "routerHostname": search_resource_names.mc_proxy_svc_fqdn(
                            self.mdbs_resource_name, self.namespace, i
                        ),
                    }
                if lb_endpoint:
                    lb["unmanaged"] = {"endpoint": lb_endpoint}
                elif lb_mode == "Unmanaged":
                    lb["unmanaged"] = {}
                cluster["loadBalancer"] = lb
            resource["spec"]["clusters"] = clusters

        return resource

    def _wire_user_api(self, resource: MongoDBUser) -> None:
        """Point the user resource at this helper's cluster when one was set.

        SC leaves ``api_client`` None (operator-cluster default); MC constructs
        the helper with the central-cluster client so the MongoDBUser lands there.
        """
        if self.api_client is not None:
            resource.api = kubernetes.client.CustomObjectsApi(self.api_client)

    def admin_user_resource(self, admin_user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-admin.yaml"),
            namespace=self.namespace,
            name=admin_user_name,
        )
        if not try_load(resource):
            resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
            resource["spec"]["username"] = resource.name
            resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        self._wire_user_api(resource)
        return resource

    def user_resource(self, user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-mdb-user.yaml"),
            namespace=self.namespace,
            name=user_name,
        )
        if not try_load(resource):
            resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
            resource["spec"]["username"] = resource.name
            resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        self._wire_user_api(resource)
        return resource

    def mongot_user_resource(self, mdbs_name: str, mongot_user_name: str) -> MongoDBUser:
        resource = MongoDBUser.from_yaml(
            yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
            namespace=self.namespace,
            name=f"{mdbs_name}-{mongot_user_name}",
        )
        if not try_load(resource):
            resource["spec"]["mongodbResourceRef"]["name"] = self.mdb_resource_name
            resource["spec"]["username"] = mongot_user_name
            resource["spec"]["passwordSecretKeyRef"]["name"] = f"{resource.name}-password"
        self._wire_user_api(resource)
        return resource

    def configure_metrics_forwarder_opsmanager(self, mdbs: MongoDBSearch, mdb: MongoDB) -> None:
        """Point the metrics forwarder at the source MongoDB's Ops Manager project.

        External sources don't expose a mongodbResourceRef, so the forwarder can't auto-resolve the
        Ops Manager project the way it does for internal sources. It must instead be given the project
        ConfigMap and the agent-credentials Secret that the operator created for the source MongoDB.
        The agent-credentials Secret is named "{projectId}-group-secret" (see agents.ApiKeySecretName).

        Must be called after the source MongoDB has reached Running so that status.projectId is set.
        """
        mdb.load()
        project_id = mdb["status"]["projectId"]
        project_config_map_name = mdb["spec"]["opsManager"]["configMapRef"]["name"]
        ensure_nested_objects(mdbs, ["spec", "observability", "metricsForwarder"])
        mdbs["spec"]["observability"]["metricsForwarder"]["opsManager"] = {
            "projectConfigMapRef": {"name": project_config_map_name},
            "agentCredentials": {"name": f"{project_id}-group-secret"},
        }

    def apply_user_password(self, user_resource: MongoDBUser, password: str) -> None:
        """Create/update the user's password secret and re-apply the CR.

        The secret lands in this helper's cluster (``self.api_client``). Does not
        wait for any phase — callers assert phases.
        """
        create_or_update_secret(
            self.namespace,
            name=user_resource["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": password},
            api_client=self.api_client,
        )
        user_resource.update()

    def deploy_users(
        self,
        admin_user: MongoDBUser,
        admin_password: str,
        user: MongoDBUser,
        user_password: str,
        mongot_user: MongoDBUser,
        mongot_password: str,
    ):
        self.apply_user_password(admin_user, admin_password)
        admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

        self.apply_user_password(user, user_password)
        user.assert_reaches_phase(Phase.Updated, timeout=300)

        self.apply_user_password(mongot_user, mongot_password)
        # Don't wait for mongot user — needs searchCoordinator role from Search CR

    def create_replicaset_mdb(
        self,
        mongot_host: Optional[str] = None,
        set_tls_ca: bool = False,
        issuer_ca_configmap: Optional[str] = None,
        tls_cert_prefix: Optional[str] = None,
    ) -> MongoDB:
        resource = MongoDB.from_yaml(
            yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
            name=self.mdb_resource_name,
            namespace=self.namespace,
        )

        if try_load(resource):
            return resource

        resource.configure(om=get_ops_manager(self.namespace), project_name=self.mdb_resource_name)

        if issuer_ca_configmap and tls_cert_prefix:
            resource.configure_custom_tls(issuer_ca_configmap, tls_cert_prefix)

        if set_tls_ca:
            resource["spec"]["security"]["tls"]["ca"] = self.ca_configmap_name

        if mongot_host is not None:
            if "additionalMongodConfig" not in resource["spec"]:
                resource["spec"]["additionalMongodConfig"] = {}
            resource["spec"]["additionalMongodConfig"]["setParameter"] = {
                "mongotHost": mongot_host,
                "searchIndexManagementHostAndPort": mongot_host,
                "skipAuthenticationToSearchIndexManagementServer": False,
                "skipAuthenticationToMongot": False,
                "searchTLSMode": "requireTLS",
                "useGrpcForSearch": True,
            }

        return resource

    def install_sharded_tls_certificates(self, secret_prefix: str = "mdb-sh-", shard_count: Optional[int] = None):
        mongos_service_dns = f"{self.mdb_resource_name}-svc.{self.namespace}.svc.cluster.local"
        create_sharded_cluster_certs(
            namespace=self.namespace,
            resource_name=self.mdb_resource_name,
            shards=shard_count if shard_count is not None else self.shard_count,
            mongod_per_shard=self.mongods_per_shard,
            config_servers=self.config_server_count,
            mongos=self.mongos_count,
            secret_prefix=secret_prefix,
            mongos_service_dns_names=[mongos_service_dns],
        )
        logger.info("Sharded cluster TLS certificates created")

    def create_rs_mdb(
        self,
        set_tls: bool = False,
        mongot_host: Optional[str] = None,
    ) -> MongoDB:
        """Create an Enterprise ReplicaSet MongoDB resource."""
        resource = MongoDB.from_yaml(
            yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
            name=self.mdb_resource_name,
            namespace=self.namespace,
        )

        if try_load(resource):
            return resource

        resource.configure(om=get_ops_manager(self.namespace), project_name=self.mdb_resource_name)

        if set_tls:
            resource.configure_custom_tls(self.ca_configmap_name, "certs")

        if mongot_host is not None:
            resource["spec"]["additionalMongodConfig"] = {
                "setParameter": {
                    "mongotHost": mongot_host,
                    "searchIndexManagementHostAndPort": mongot_host,
                    "skipAuthenticationToSearchIndexManagementServer": False,
                    "skipAuthenticationToMongot": False,
                    "searchTLSMode": "requireTLS",
                    "useGrpcForSearch": True,
                }
            }

        return resource

    def install_rs_tls_certificates(self, issuer: str, members: int = 3):
        """Create MongoDB RS TLS certificates."""
        create_mongodb_tls_certs(
            issuer,
            self.namespace,
            self.mdb_resource_name,
            f"certs-{self.mdb_resource_name}-cert",
            members,
        )
        logger.info("RS TLS certificates created")

    def mdbs_for_ext_rs_source(
        self,
        mongot_user_name: str,
        members: int = 3,
        lb_mode: Optional[str] = None,
        replicas: Optional[int] = None,
        clusters: Optional[list] = None,
    ) -> MongoDBSearch:
        """Create MongoDBSearch with an external RS source.

        ``clusters`` (mutually exclusive with ``replicas``) writes spec.clusters;
        a single entry with no name models a single-cluster RS at index 0.
        """
        resource = MongoDBSearch.from_yaml(
            yaml_fixture("search-minimal.yaml"),
            namespace=self.namespace,
            name=self.mdbs_resource_name,
        )

        if try_load(resource):
            return resource

        seeds = [
            f"{self.mdb_resource_name}-{i}.{self.mdb_resource_name}-svc.{self.namespace}.svc.cluster.local:27017"
            for i in range(members)
        ]

        resource["spec"]["source"] = {
            "username": mongot_user_name,
            "passwordSecretRef": {
                "name": f"{self.mdbs_resource_name}-{mongot_user_name}-password",
                "key": "password",
            },
            "external": {
                "hostAndPorts": seeds,
                "tls": {"ca": {"name": self.ca_configmap_name}},
            },
        }

        resource["spec"]["security"] = {
            "tls": {"certsSecretPrefix": self.tls_cert_prefix},
        }

        if clusters is not None:
            resource["spec"]["clusters"] = clusters
        elif replicas is not None:
            resource["spec"]["clusters"] = [{"replicas": replicas}]

        if lb_mode:
            clusters_spec = resource["spec"].get("clusters") or [{}]
            for i, cluster in enumerate(clusters_spec):
                if lb_mode == "Managed":
                    cluster["loadBalancer"] = {
                        "managed": {
                            "externalHostname": search_resource_names.mc_proxy_svc_fqdn(
                                self.mdbs_resource_name, self.namespace, i
                            )
                        }
                    }
                elif lb_mode == "Unmanaged":
                    cluster["loadBalancer"] = {"unmanaged": {}}
            resource["spec"]["clusters"] = clusters_spec

        return resource


class MCSearchDeploymentHelper:
    """Per-cluster index and proxy-svc FQDN lookups for MC search e2e tests.

    Cluster ordering follows dict-insertion order of `member_cluster_clients`,
    which mirrors the explicit clusterIndex pins the e2e builders set on
    spec.clusters[] (0..N-1 in the same client order).
    """

    def __init__(
        self,
        namespace: str,
        mdbs_resource_name: str,
        member_cluster_clients: Mapping[str, CoreV1Api],
    ) -> None:
        self.namespace = namespace
        self.mdbs_resource_name = mdbs_resource_name
        self._member_cluster_clients = dict(member_cluster_clients)
        self._cluster_indices = {name: idx for idx, name in enumerate(self._member_cluster_clients)}

    def member_cluster_names(self) -> list[str]:
        return list(self._member_cluster_clients.keys())

    def cluster_index(self, cluster_name: str) -> int:
        if cluster_name not in self._cluster_indices:
            raise KeyError(f"unknown member cluster: {cluster_name!r}")
        return self._cluster_indices[cluster_name]

    def member_clients(self) -> Mapping[str, CoreV1Api]:
        return self._member_cluster_clients

    def proxy_svc_fqdn(self, cluster_name: str) -> str:
        return search_resource_names.mc_proxy_svc_fqdn(
            self.mdbs_resource_name, self.namespace, self.cluster_index(cluster_name)
        )
