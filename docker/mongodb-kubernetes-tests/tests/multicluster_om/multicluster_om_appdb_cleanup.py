from typing import List

import kubernetes
import kubernetes.client
from kubetester import try_load
from kubetester.awss3client import AwsS3Client
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import ensure_ent_version, run_periodically
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests import test_logger
from tests.common.ops_manager.multi_cluster import (
    ops_manager_multi_cluster_with_tls_s3_backups,
)
from tests.conftest import (
    create_appdb_certs,
    get_central_cluster_client,
    get_cluster_clients,
    get_member_cluster_clients,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_appdb.conftest import (
    create_s3_bucket_blockstore,
    create_s3_bucket_oplog,
)

CERT_PREFIX = "prefix"
OM_NAME = "om-appdb-cleanup"

logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]


@fixture(scope="module")
def om_member_cluster_names() -> list[str]:
    return ["kind-e2e-cluster-1", "kind-e2e-cluster-3"]


@fixture(scope="module")
def s3_bucket_blockstore(namespace: str, aws_s3_client: AwsS3Client) -> str:
    return next(create_s3_bucket_blockstore(namespace, aws_s3_client, api_client=get_central_cluster_client()))


@fixture(scope="module")
def s3_bucket_oplog(namespace: str, aws_s3_client: AwsS3Client) -> str:
    return next(create_s3_bucket_oplog(namespace, aws_s3_client, api_client=get_central_cluster_client()))


@fixture(scope="module")
def ops_manager_certs(namespace: str, multi_cluster_issuer: str):
    return create_ops_manager_tls_certs(
        multi_cluster_issuer,
        namespace,
        OM_NAME,
        secret_name=f"{CERT_PREFIX}-{OM_NAME}-cert",
    )


@fixture(scope="module")
def appdb_certs_secret(namespace: str, multi_cluster_issuer: str):
    return create_appdb_certs(
        namespace,
        multi_cluster_issuer,
        OM_NAME + "-db",
        cluster_index_with_members=[(0, 5), (1, 5), (2, 5)],
        cert_prefix=CERT_PREFIX,
    )


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    multi_cluster_issuer_ca_configmap: str,
    appdb_member_cluster_names: list[str],
    om_member_cluster_names: list[str],
    s3_bucket_blockstore: str,
    s3_bucket_oplog: str,
    appdb_certs_secret: str,
    ops_manager_certs: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource = ops_manager_multi_cluster_with_tls_s3_backups(
        namespace, OM_NAME, central_cluster_client, custom_appdb_version, s3_bucket_blockstore, s3_bucket_oplog
    )

    if try_load(resource):
        return resource

    resource.set_version(custom_version)

    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        om_member_cluster_names, [1, 1], backup_configs=[{"members": 1}, {"members": 1}]
    )
    resource["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": 9000,
    }

    resource["spec"]["applicationDatabase"] = {
        "version": custom_appdb_version,
        "topology": "MultiCluster",
        "clusterSpecList": cluster_spec_list(appdb_member_cluster_names, [1, 2]),
        "agent": {"logLevel": "DEBUG"},
        "security": {
            "certsSecretPrefix": CERT_PREFIX,
            "tls": {"ca": multi_cluster_issuer_ca_configmap},
        },
        "externalAccess": {
            "externalService": {
                "spec": {
                    "type": "LoadBalancer",
                    "ports": [
                        {
                            "name": "mongodb",
                            "port": 27017,
                        },
                        {
                            "name": "backup",
                            "port": 27018,
                        },
                        {
                            "name": "testing2",
                            "port": 27019,
                        },
                    ],
                }
            },
        },
    }

    return resource


@mark.e2e_multi_cluster_appdb_cleanup
def test_deploy_operator(multi_cluster_operator_with_monitored_appdb: Operator):
    multi_cluster_operator_with_monitored_appdb.assert_is_running()


@mark.e2e_multi_cluster_appdb_cleanup
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)


@mark.e2e_multi_cluster_appdb_cleanup
def test_delete_ops_manager_resource(ops_manager: MongoDBOpsManager):
    ops_manager.delete()

    def resource_is_deleted() -> bool:
        try:
            ops_manager.load()
            return False
        except kubernetes.client.ApiException as e:
            if e.status == 404:
                return True
            else:
                logger.error(f"Error when trying to load the opsmanager {ops_manager.name} resource: {e}")
                return False

    run_periodically(resource_is_deleted, timeout=60)


@mark.e2e_multi_cluster_appdb_cleanup
def test_statefulset_does_not_exist(ops_manager: MongoDBOpsManager):
    def sts_are_deleted() -> bool:
        for cluster_member_client in get_member_cluster_clients():
            sts = cluster_member_client.list_namespaced_stateful_sets(ops_manager.namespace)
            if len(sts.items) != 0:
                return False

        return True

    run_periodically(sts_are_deleted, timeout=60)


@mark.e2e_multi_cluster_appdb_cleanup
def test_service_does_not_exist(ops_manager: MongoDBOpsManager):
    def svc_are_deleted() -> bool:
        excluded_services = ["operator-webhook", "mongodb-enterprise-operator"]

        for cluster_member_client in get_member_cluster_clients():
            svc = cluster_member_client.list_namespaced_services(ops_manager.namespace)
            svc_names = [item.metadata.name for item in svc.items]
            svc_items_filtered = list(filter(lambda x: x not in excluded_services, svc_names))

            if len(svc_items_filtered) > 0:
                logger.error(f"Services still exist: {svc_items_filtered}")
                return False

        return True

    run_periodically(svc_are_deleted, timeout=60)


@mark.e2e_multi_cluster_appdb_cleanup
def test_configmap_does_not_exist(ops_manager: MongoDBOpsManager):
    def cm_are_deleted() -> bool:
        for cluster_member_client in get_member_cluster_clients():
            cm = cluster_member_client.list_namespaced_config_maps(ops_manager.namespace)
            cm_names = [item.metadata.name for item in cm.items]

            # Check configmaps related to the ops manager by filtering by name
            cm_names_filtered = list(filter(lambda x: x.startswith(ops_manager.name), cm_names))
            if len(cm_names_filtered) > 0:
                logger.error(f"ConfigMaps still exist: {cm_names_filtered}")
                return False

        return True

    run_periodically(cm_are_deleted, timeout=60)
