from kubetester import find_fixture, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_member_cluster_clients, get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list
from tests.shardedcluster.conftest import get_member_cluster_clients_using_cluster_mapping

MDB_RESOURCE_NAME = "sh"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    try_load(resource)
    return resource


@mark.e2e_multi_cluster_sharded_simplest
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_multi_cluster_sharded_simplest
def test_create(sharded_cluster: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
    sharded_cluster.set_version(ensure_ent_version(custom_mdb_version))

    sharded_cluster["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 1])
    sharded_cluster.set_architecture_annotation()
    sharded_cluster.update()


@mark.e2e_multi_cluster_sharded_simplest
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_multi_cluster_sharded_simplest
def test_statefulsets_multi_cluster_identity(namespace: str):
    """Regression test: sharded cluster StatefulSets in member clusters must carry no
    ownerReferences and must carry the MongoDBMultiResource annotation.

    No ownerReferences: a cross-cluster ownerReference points to the MongoDBShardedCluster
    CR that only exists in the central cluster. The Kubernetes GC treats the StatefulSet as
    an orphan and deletes it immediately, causing an infinite create-delete reconciliation loop.
    Cleanup on CR deletion is handled through explicit label-based deletion instead.

    MongoDBMultiResource annotation: replaces ownerReferences as the identifier that watch
    predicates and the OM connection factory use to map StatefulSets back to their parent CR."""
    for mcc in get_member_cluster_clients():
        sts_list = mcc.list_namespaced_stateful_sets(namespace)
        for sts in sts_list.items:
            owner_refs = sts.metadata.owner_references
            assert not owner_refs, (
                f"StatefulSet {sts.metadata.name} in cluster {mcc.cluster_name} must have no "
                f"ownerReferences in multi-cluster mode, but got: {owner_refs}"
            )
            annotation_value = (sts.metadata.annotations or {}).get("MongoDBMultiResource")
            assert annotation_value == MDB_RESOURCE_NAME, (
                f"StatefulSet {sts.metadata.name} in cluster {mcc.cluster_name} must carry "
                f"annotation 'MongoDBMultiResource={MDB_RESOURCE_NAME}', but got: {annotation_value!r}"
            )


@mark.e2e_multi_cluster_sharded_simplest
def test_internal_connectivity_annotations(sharded_cluster: MongoDB):
    """spec.internalConnectivity.annotations are merged onto the internal Services of a
    multi-cluster sharded cluster: the top-level value lands on every tier in every member
    cluster, while a per-member-cluster entry on the shard tier overrides individual keys in
    its own cluster only (with clusterName/clusterIndex placeholders substituted).

    Pod-scoped placeholders (e.g. {podName}) are substituted on the per-pod Services and are
    omitted from the shared per-tier headless Services, where they have no meaning."""
    override_cluster_name = sharded_cluster["spec"]["shard"]["clusterSpecList"][0]["clusterName"]
    sharded_cluster["spec"]["internalConnectivity"] = {
        "annotations": {
            "service.cilium.io/global": "true",
            "example.com/conflict": "top-level",
            "example.com/pod": "{podName}",
        }
    }
    sharded_cluster["spec"]["shard"]["clusterSpecList"][0]["internalConnectivity"] = {
        "annotations": {
            "example.com/conflict": "cluster-override",
            "example.com/cluster-only": "{clusterName}-{clusterIndex}",
        }
    }
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)

    for mcc in get_member_cluster_clients_using_cluster_mapping(sharded_cluster.name, sharded_cluster.namespace):
        # (statefulset name, members in this cluster, whether this is the shard tier)
        tiers = [
            (
                sharded_cluster.config_srv_statefulset_name(mcc.cluster_index),
                sharded_cluster.config_srv_members_in_cluster(mcc.cluster_name),
                False,
            ),
            (
                sharded_cluster.mongos_statefulset_name(mcc.cluster_index),
                sharded_cluster.mongos_members_in_cluster(mcc.cluster_name),
                False,
            ),
            (
                sharded_cluster.shard_statefulset_name(0, mcc.cluster_index),
                sharded_cluster.shard_members_in_cluster(mcc.cluster_name),
                True,
            ),
        ]
        for sts_name, members, is_shard in tiers:
            expected_annotations = {
                "service.cilium.io/global": "true",
                "example.com/conflict": "top-level",
            }
            override_applies = is_shard and mcc.cluster_name == override_cluster_name
            if override_applies:
                expected_annotations["example.com/conflict"] = "cluster-override"
                expected_annotations["example.com/cluster-only"] = f"{mcc.cluster_name}-{mcc.cluster_index}"

            for pod_idx in range(members):
                service_name = f"{sts_name}-{pod_idx}-svc"
                svc = mcc.read_namespaced_service(service_name, sharded_cluster.namespace)
                annotations = svc.metadata.annotations or {}
                for key, value in expected_annotations.items():
                    assert annotations.get(key) == value, (
                        f"Service {service_name} in cluster {mcc.cluster_name} must carry "
                        f"annotation '{key}={value}', but got: {annotations.get(key)!r}"
                    )
                # the pod-scoped placeholder is substituted with the pod name on per-pod Services
                assert annotations.get("example.com/pod") == service_name[: -len("-svc")], (
                    f"Service {service_name} in cluster {mcc.cluster_name} must carry "
                    f"annotation 'example.com/pod={service_name[: -len('-svc')]}', "
                    f"but got: {annotations.get('example.com/pod')!r}"
                )
                if is_shard and not override_applies:
                    assert "example.com/cluster-only" not in annotations, (
                        f"Service {service_name} in cluster {mcc.cluster_name} must not carry "
                        f"the per-cluster override annotation, but got: {annotations!r}"
                    )

            # the shared per-tier headless Service: pod-scoped placeholders are omitted there
            headless_service_name = f"{sts_name}-svc"
            svc = mcc.read_namespaced_service(headless_service_name, sharded_cluster.namespace)
            annotations = svc.metadata.annotations or {}
            for key, value in expected_annotations.items():
                assert annotations.get(key) == value, (
                    f"Service {headless_service_name} in cluster {mcc.cluster_name} must carry "
                    f"annotation '{key}={value}', but got: {annotations.get(key)!r}"
                )
            assert "example.com/pod" not in annotations, (
                f"Service {headless_service_name} in cluster {mcc.cluster_name} must not carry "
                f"the pod-scoped annotation, but got: {annotations!r}"
            )
            if is_shard and not override_applies:
                assert "example.com/cluster-only" not in annotations, (
                    f"Service {headless_service_name} in cluster {mcc.cluster_name} must not carry "
                    f"the per-cluster override annotation, but got: {annotations!r}"
                )
