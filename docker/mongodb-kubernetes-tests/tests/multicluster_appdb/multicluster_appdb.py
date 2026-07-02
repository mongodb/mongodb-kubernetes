import kubernetes
import kubernetes.client
import pytest
from kubetester import delete_statefulset, wait_for_statefulset_ready, wait_for_statefulset_recreated
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import get_member_cluster_api_client
from tests.multicluster.conftest import cluster_spec_list

CERT_PREFIX = "prefix"


def appdb_cluster_spec_list_with_overrides(cluster_names: list[str], members: list[int | None]) -> list[dict]:
    """Wraps cluster_spec_list and re-applies the per-cluster hostAliases override
    so it is preserved across reconciliations (KUBE-47)."""
    spec_list = cluster_spec_list(cluster_names, members)
    for item in spec_list:
        item["statefulSet"] = {
            "spec": {
                "template": {
                    "spec": {"hostAliases": [{"ip": "127.0.0.1", "hostnames": [f"appdb-{item['clusterName']}.local"]}]}
                }
            }
        }
    return spec_list


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]


@fixture(scope="module")
def ops_manager_unmarshalled(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    multi_cluster_issuer_ca_configmap: str,
    appdb_member_cluster_names: list[str],
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["version"] = custom_version
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = cluster_spec_list(["kind-e2e-cluster-2", "kind-e2e-cluster-3"], [2, 2])

    resource.allow_mdb_rc_versions()
    resource.create_admin_secret(api_client=central_cluster_client)

    resource["spec"]["backup"] = {"enabled": False}
    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "clusterSpecList": appdb_cluster_spec_list_with_overrides(appdb_member_cluster_names, [2, 3]),
        "version": custom_appdb_version,
        "agent": {"logLevel": "DEBUG"},
        "security": {
            "certsSecretPrefix": CERT_PREFIX,
            "tls": {"ca": multi_cluster_issuer_ca_configmap},
        },
    }

    return resource


@fixture(scope="module")
def appdb_certs_secret(
    namespace: str,
    multi_cluster_issuer: str,
    ops_manager_unmarshalled: MongoDBOpsManager,
):
    return create_appdb_certs(
        namespace,
        multi_cluster_issuer,
        ops_manager_unmarshalled.name + "-db",
        cluster_index_with_members=[(0, 5), (1, 5), (2, 5)],
        cert_prefix=CERT_PREFIX,
    )


@fixture(scope="module")
def ops_manager(
    appdb_certs_secret: str,
    ops_manager_unmarshalled: MongoDBOpsManager,
) -> MongoDBOpsManager:
    ops_manager_unmarshalled.update()
    return ops_manager_unmarshalled


@mark.e2e_multi_cluster_appdb
def test_patch_central_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    corev1 = kubernetes.client.CoreV1Api(api_client=central_cluster_client)
    ns = corev1.read_namespace(namespace)
    ns.metadata.labels["istio-injection"] = "enabled"
    corev1.patch_namespace(namespace, ns)


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_appdb
def test_create_om(ops_manager: MongoDBOpsManager, appdb_member_cluster_names: list[str]):
    ops_manager.load()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.assert_appdb_preferred_hostnames_are_added()
    ops_manager.assert_appdb_hostnames_are_correct()

    # Verify the per-cluster statefulSet override (hostAliases) set in the fixture
    # was merged into each cluster's AppDB StatefulSet.
    for cluster_name in appdb_member_cluster_names:
        sts = ops_manager.read_appdb_statefulset(member_cluster_name=cluster_name)
        host_aliases = sts.spec.template.spec.host_aliases or []
        hostnames = [h for alias in host_aliases for h in (alias.hostnames or [])]
        assert (
            f"appdb-{cluster_name}.local" in hostnames
        ), f"per-cluster hostAlias missing on AppDB STS in {cluster_name}: got {hostnames}"


@mark.e2e_multi_cluster_appdb
def test_appdb_statefulsets_multi_cluster_identity(
    ops_manager: MongoDBOpsManager,
    appdb_member_cluster_names: list[str],
):
    """Regression test: AppDB StatefulSets in member clusters must carry no ownerReferences.

    A cross-cluster ownerReference points to the MongoDBOpsManager CR that only exists in
    the central cluster. The Kubernetes GC treats the StatefulSet as an orphan and deletes
    it immediately, causing an infinite create-delete reconciliation loop. Cleanup on CR
    deletion is handled through explicit label-based deletion instead."""
    for cluster_name in appdb_member_cluster_names:
        sts = ops_manager.read_appdb_statefulset(member_cluster_name=cluster_name)
        owner_refs = sts.metadata.owner_references
        assert not owner_refs, (
            f"AppDB StatefulSet {sts.metadata.name} in cluster {cluster_name} must have no "
            f"ownerReferences in multi-cluster mode, but got: {owner_refs}"
        )


@mark.e2e_multi_cluster_appdb
def test_appdb_statefulset_watch_recreates_deleted_member(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    appdb_member_cluster_names: list[str],
):
    """Deleting an AppDB StatefulSet in a member cluster must trigger the operator's member-cluster
    StatefulSet watch, which reconciles the MongoDBOpsManager and recreates the StatefulSet. A Running
    resource requeues only after 24h, so a prompt recreation proves the watch drove the reconcile
    rather than the periodic resync."""
    cluster_name = appdb_member_cluster_names[0]
    api_client = get_member_cluster_api_client(cluster_name)
    sts_old = ops_manager.read_appdb_statefulset(member_cluster_name=cluster_name)
    sts_name = sts_old.metadata.name
    # The multi-cluster member watch maps the StatefulSet to its parent CR through the
    # MongoDBMultiResource annotation (member StatefulSets carry no ownerReference), so the
    # annotation must be present for the watch to trigger a reconcile.
    assert "MongoDBMultiResource" in (sts_old.metadata.annotations or {}), (
        f"AppDB StatefulSet {sts_name} in cluster {cluster_name} must carry the MongoDBMultiResource "
        f"annotation, but had annotations={sts_old.metadata.annotations}"
    )

    delete_statefulset(namespace, sts_name, api_client=api_client)
    wait_for_statefulset_recreated(namespace, sts_name, sts_old.metadata.uid, api_client=api_client)
    wait_for_statefulset_ready(namespace, sts_name, api_client=api_client, timeout=800)

    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_multi_cluster_appdb
def test_scale_up_one_cluster(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        appdb_member_cluster_names, [4, 3]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.assert_appdb_preferred_hostnames_are_added()
    ops_manager.assert_appdb_hostnames_are_correct()


@mark.e2e_multi_cluster_appdb
def test_scale_down_one_cluster(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        appdb_member_cluster_names, [4, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_hosts_removed_after_scale_down_one_cluster(ops_manager: MongoDBOpsManager):
    """Verifies that scaled-down AppDB hosts are removed from OM monitoring."""
    ops_manager.assert_appdb_hostnames_are_correct()


@mark.e2e_multi_cluster_appdb
def test_scale_up_two_clusters(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        appdb_member_cluster_names, [5, 2]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_scale_down_two_clusters(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        appdb_member_cluster_names, [2, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_add_cluster_to_cluster_spec(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    cluster_names = ["kind-e2e-cluster-1"] + appdb_member_cluster_names
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        cluster_names, [2, 2, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_remove_cluster_from_cluster_spec(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    # Before removing, we need to scale down the cluster to zero
    ops_manager.load()
    cluster_names = ["kind-e2e-cluster-1"] + appdb_member_cluster_names
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        cluster_names, [2, 0, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    # Now we can remove the cluster from the spec
    ops_manager.load()
    cluster_names = ["kind-e2e-cluster-1"] + appdb_member_cluster_names[1:]
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        cluster_names, [2, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_read_cluster_to_cluster_spec(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    cluster_names = ["kind-e2e-cluster-1"] + appdb_member_cluster_names
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = appdb_cluster_spec_list_with_overrides(
        cluster_names, [2, 2, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
