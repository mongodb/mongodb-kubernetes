import kubernetes
import kubernetes.client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import create_appdb_certs
from tests.multicluster.conftest import cluster_spec_list

CERT_PREFIX = "prefix"


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
        "clusterSpecList": cluster_spec_list(appdb_member_cluster_names, [2, 3]),
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
    resource = ops_manager_unmarshalled.update()
    return resource


@mark.e2e_multi_cluster_appdb
def test_patch_central_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    corev1 = kubernetes.client.CoreV1Api(api_client=central_cluster_client)
    ns = corev1.read_namespace(namespace)
    ns.metadata.labels["istio-injection"] = "enabled"
    corev1.patch_namespace(namespace, ns)


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_appdb
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_scale_up_one_cluster(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        appdb_member_cluster_names, [4, 3]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_scale_down_one_cluster(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        appdb_member_cluster_names, [4, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_scale_up_two_clusters(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        appdb_member_cluster_names, [5, 2]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_scale_down_two_clusters(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        appdb_member_cluster_names, [2, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_add_cluster_to_cluster_spec(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    cluster_names = ["kind-e2e-cluster-1"] + appdb_member_cluster_names
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(cluster_names, [2, 2, 1])
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_remove_cluster_from_cluster_spec(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    cluster_names = ["kind-e2e-cluster-1"] + appdb_member_cluster_names[1:]
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(cluster_names, [2, 1])
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb
def test_readd_cluster_to_cluster_spec(ops_manager: MongoDBOpsManager, appdb_member_cluster_names):
    ops_manager.load()
    cluster_names = ["kind-e2e-cluster-1"] + appdb_member_cluster_names
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(cluster_names, [2, 2, 1])
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
