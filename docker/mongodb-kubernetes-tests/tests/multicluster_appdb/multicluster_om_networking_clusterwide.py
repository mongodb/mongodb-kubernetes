import kubernetes
from kubetester import read_secret, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.multicluster_client import MultiClusterClient
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    create_issuer_ca_configmap,
    get_central_cluster_client,
    get_central_cluster_name,
    get_issuer_ca_filepath,
    install_multi_cluster_operator_cluster_scoped,
)
from tests.multicluster import prepare_multi_cluster_namespaces
from tests.multicluster.conftest import cluster_spec_list, create_namespace

from ..common.constants import MEMBER_CLUSTER_1, MEMBER_CLUSTER_2, MEMBER_CLUSTER_3
from ..common.ops_manager.multi_cluster import (
    ops_manager_multi_cluster_with_tls_s3_backups,
)
from .conftest import create_s3_bucket_blockstore, create_s3_bucket_oplog

# This test is for checking networking when OM is deployed in a complex multi-cluster scenario involving:
#  - OM deployed in different namespace than the operator
#  - OM deployed in different clusters than the operator
#  - with TLS and custom CAs for AppDB and OM
#  - AppDB in MultiCluster mode, but limited to a single member cluster for simplicity
#  - S3 backups enabled
#  - OM's external connectivity enabled

OM_NAMESPACE = "mdb-om-mc"
OM_NAME = "om-mc"
OM_CERT_PREFIX = "om-prefix"
APPDB_CERT_PREFIX = "appdb-prefix"


@fixture(scope="module")
def om_issuer_ca_configmap() -> str:
    return create_issuer_ca_configmap(
        get_issuer_ca_filepath(), namespace=OM_NAMESPACE, name="om-issuer-ca", api_client=get_central_cluster_client()
    )


@fixture(scope="module")
def appdb_issuer_ca_configmap() -> str:
    return create_issuer_ca_configmap(
        get_issuer_ca_filepath(),
        namespace=OM_NAMESPACE,
        name="appdb-issuer-ca",
        api_client=get_central_cluster_client(),
    )


@fixture(scope="module")
def ops_manager_certs(multi_cluster_clusterissuer: str):
    return create_ops_manager_tls_certs(
        multi_cluster_clusterissuer,
        OM_NAMESPACE,
        OM_NAME,
        secret_name=f"{OM_CERT_PREFIX}-{OM_NAME}-cert",
        clusterwide=True,
    )


@fixture(scope="module")
def appdb_certs(multi_cluster_clusterissuer: str):
    from tests.common.cert.cert_issuer import create_appdb_certs

    return create_appdb_certs(
        OM_NAMESPACE,
        multi_cluster_clusterissuer,
        OM_NAME + "-db",
        cluster_index_with_members=[(0, 3)],
        cert_prefix=APPDB_CERT_PREFIX,
        clusterwide=True,
    )


@fixture(scope="module")
def s3_bucket_blockstore(aws_s3_client: AwsS3Client) -> str:
    return next(create_s3_bucket_blockstore(OM_NAMESPACE, aws_s3_client, api_client=get_central_cluster_client()))


@fixture(scope="module")
def s3_bucket_oplog(aws_s3_client: AwsS3Client) -> str:
    return next(create_s3_bucket_oplog(OM_NAMESPACE, aws_s3_client, api_client=get_central_cluster_client()))


@fixture(scope="function")
def ops_manager(
    custom_version: str,
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
    ops_manager_certs: str,
    appdb_certs: str,
    issuer_ca_filepath: str,
    s3_bucket_blockstore: str,
    s3_bucket_oplog: str,
    om_issuer_ca_configmap: str,
    appdb_issuer_ca_configmap: str,
) -> MongoDBOpsManager:
    resource = ops_manager_multi_cluster_with_tls_s3_backups(
        OM_NAMESPACE, OM_NAME, central_cluster_client, custom_appdb_version, s3_bucket_blockstore, s3_bucket_oplog
    )

    if try_load(resource):
        return resource

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["version"] = custom_version
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = cluster_spec_list([MEMBER_CLUSTER_2], [1], backup_configs=[{"members": 1}])
    resource["spec"]["security"] = {
        "certsSecretPrefix": OM_CERT_PREFIX,
        "tls": {
            "ca": om_issuer_ca_configmap,
        },
    }

    resource.create_admin_secret(api_client=central_cluster_client)

    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "clusterSpecList": cluster_spec_list([MEMBER_CLUSTER_3], [3]),
        "version": custom_appdb_version,
        "agent": {"logLevel": "DEBUG"},
        "security": {
            "certsSecretPrefix": APPDB_CERT_PREFIX,
            "tls": {
                "ca": appdb_issuer_ca_configmap,
            },
        },
    }

    return resource


@mark.e2e_multi_cluster_om_networking_clusterwide
def test_create_namespace(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: list[MultiClusterClient],
    evergreen_task_id: str,
    multi_cluster_operator_installation_config: dict[str, str],
):
    image_pull_secret_name = multi_cluster_operator_installation_config["registry.imagePullSecrets"]
    image_pull_secret_data = read_secret(namespace, image_pull_secret_name, api_client=central_cluster_client)

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        OM_NAMESPACE,
        image_pull_secret_name,
        image_pull_secret_data,
    )

    prepare_multi_cluster_namespaces(
        OM_NAMESPACE,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        get_central_cluster_name(),
        True,
    )


@mark.e2e_multi_cluster_om_networking_clusterwide
def test_deploy_operator(namespace: str):
    install_multi_cluster_operator_cluster_scoped(watch_namespaces=[namespace, OM_NAMESPACE])


@mark.e2e_multi_cluster_om_networking_clusterwide
def test_deploy_ops_manager(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_om_networking_clusterwide
def test_scale_om_on_different_cluster(ops_manager: MongoDBOpsManager):
    ops_manager["spec"]["clusterSpecList"] = [
        {
            "clusterName": MEMBER_CLUSTER_2,
            "members": 1,
            "backup": {
                "members": 1,
            },
        },
        {
            "clusterName": MEMBER_CLUSTER_3,
            "members": 2,
        },
    ]

    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_om_networking_clusterwide
def test_scale_backup_daemon_on_different_cluster(ops_manager: MongoDBOpsManager):
    ops_manager["spec"]["clusterSpecList"] = [
        {
            "clusterName": MEMBER_CLUSTER_2,
            "members": 1,
            "backup": {
                "members": 2,
            },
        },
        {
            "clusterName": MEMBER_CLUSTER_3,
            "members": 2,
            "backup": {
                "members": 1,
            },
        },
    ]

    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_om_networking_clusterwide
def test_enable_external_connectivity(ops_manager: MongoDBOpsManager):
    ops_manager["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": 9000,
    }
    # override the default port for the first cluster
    ops_manager["spec"]["clusterSpecList"][0]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": 5000,
    }
    # override the service type for the second cluster
    ops_manager["spec"]["clusterSpecList"].append(
        {
            "clusterName": MEMBER_CLUSTER_1,
            "members": 1,
            "backup": {
                "members": 1,
            },
            "externalConnectivity": {
                "type": "NodePort",
                "port": 30006,
                "annotations": {
                    "test-annotation": "test-value",
                },
            },
        }
    )

    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_om_networking_clusterwide
def test_external_services_are_created(ops_manager: MongoDBOpsManager):
    _, external = ops_manager.services(MEMBER_CLUSTER_1)
    assert external.spec.type == "NodePort"
    assert external.metadata.annotations == {"test-annotation": "test-value"}
    assert len(external.spec.ports) == 2
    assert external.spec.ports[0].port == 30006
    assert external.spec.ports[0].target_port == 8443

    _, external = ops_manager.services(MEMBER_CLUSTER_2)
    assert external.spec.type == "LoadBalancer"
    assert len(external.spec.ports) == 2
    assert external.spec.ports[0].port == 5000
    assert external.spec.ports[0].target_port == 8443

    _, external = ops_manager.services(MEMBER_CLUSTER_3)
    assert external.spec.type == "LoadBalancer"
    assert len(external.spec.ports) == 2
    assert external.spec.ports[0].port == 9000
    assert external.spec.ports[0].target_port == 8443
