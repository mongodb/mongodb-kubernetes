from __future__ import annotations

from typing import Optional

import kubernetes.client
from kubetester import create_or_update_configmap
from kubetester.awss3client import s3_endpoint
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.opsmanager import MongoDBOpsManager
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.common.constants import S3_BLOCKSTORE_NAME, S3_OPLOG_NAME
from tests.conftest import get_member_cluster_api_client
from tests.constants import AWS_REGION
from tests.multicluster.conftest import cluster_spec_list

PRIMARY_OM_NAME = "primary-om"
APPDB_NAME = f"{PRIMARY_OM_NAME}-db"
APPDB_CERT_PREFIX = "appdb"
APPDB_MEMBER_CLUSTER_NAMES = ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]
APPDB_MEMBER_COUNTS: list[int | None] = [2, 2]


def appdb_ca_configmap(multi_cluster_issuer_ca_configmap: str) -> str:
    return multi_cluster_issuer_ca_configmap


def appdb_cert_prefix(namespace: str, multi_cluster_issuer: str) -> str:
    return create_appdb_certs(
        namespace,
        multi_cluster_issuer,
        APPDB_NAME,
        cluster_index_with_members=[(0, 2), (1, 2)],
        cert_prefix=APPDB_CERT_PREFIX,
    )


def appdb_member_cluster_names() -> list[str]:
    return APPDB_MEMBER_CLUSTER_NAMES.copy()


def multi_cluster_appdb_role_resource(
    namespace: str,
    custom_mdb_version: str,
    appdb_cert_prefix: str,
    appdb_ca_configmap: str,
    appdb_member_cluster_names: list[str],
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi-cluster.yaml"), name=APPDB_NAME, namespace=namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["role"] = "AppDB"
    resource["spec"]["persistent"] = True
    resource["spec"]["clusterSpecList"] = cluster_spec_list(appdb_member_cluster_names, APPDB_MEMBER_COUNTS)
    resource.configure_custom_tls(appdb_ca_configmap, appdb_cert_prefix)
    return resource


def configure_appdb_role_mongodb_multi(
    mdb: MongoDBMulti, meta_om: MongoDBOpsManager, namespace: str, appdb_ca_configmap: str
) -> MongoDBMulti:
    project_name = f"{mdb.name}-project"
    config_map_name = f"{mdb.name}-config"
    base_url = meta_om.om_status().get_url()
    assert base_url is not None, "OpsManager URL must not be None"

    # The AppDB pods are TLS-enabled, so the OM connection config must advertise the CA their agents
    # should trust when reaching Ops Manager, mirroring the working multi-cluster AppDB tests.
    create_or_update_configmap(
        meta_om.namespace,
        config_map_name,
        {
            "baseUrl": base_url,
            "projectName": project_name,
            "sslMMSCAConfigMap": appdb_ca_configmap,
            "orgId": "",
        },
        None,
    )

    mdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
    mdb["spec"]["credentials"] = meta_om.api_key_secret(namespace)

    return mdb


def configure_appdb_backup_mongodb_ops_manager(
    resource: MongoDBOpsManager, s3_bucket_blockstore: str, s3_bucket_oplog: str
) -> MongoDBOpsManager:
    resource["spec"]["backup"]["enabled"] = True
    resource["spec"]["backup"]["s3Stores"] = [
        {
            "name": S3_BLOCKSTORE_NAME,
            "s3SecretRef": {"name": f"{S3_BLOCKSTORE_NAME}-secret"},
            "pathStyleAccessEnabled": True,
            "s3BucketEndpoint": s3_endpoint(AWS_REGION),
            "s3BucketName": s3_bucket_blockstore,
            "s3RegionOverride": AWS_REGION,
        }
    ]
    resource["spec"]["backup"]["s3OpLogStores"] = [
        {
            "name": S3_OPLOG_NAME,
            "s3SecretRef": {"name": f"{S3_OPLOG_NAME}-secret"},
            "pathStyleAccessEnabled": True,
            "s3BucketEndpoint": s3_endpoint(AWS_REGION),
            "s3BucketName": s3_bucket_oplog,
            "s3RegionOverride": AWS_REGION,
        }
    ]
    resource["spec"]["statefulSet"] = {
        "spec": {
            "template": {
                "spec": {
                    "containers": [
                        {
                            "name": "mongodb-ops-manager",
                            "resources": {"requests": {"memory": "15G"}, "limits": {"memory": "15G"}},
                        }
                    ]
                }
            }
        }
    }

    return resource


def primary_om_resource(namespace: str, custom_version: Optional[str]) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary_om_no_appdb.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource["spec"]["externalApplicationDatabaseRef"] = {"name": APPDB_NAME, "kind": "MongoDBMultiCluster"}
    return resource


def primary_om_internal_appdb_resource(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    appdb_ca_configmap: str,
    appdb_cert_prefix: str,
    appdb_member_cluster_names: list[str],
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), name=PRIMARY_OM_NAME, namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = cluster_spec_list(appdb_member_cluster_names, APPDB_MEMBER_COUNTS)
    resource["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        appdb_member_cluster_names, APPDB_MEMBER_COUNTS
    )
    resource["spec"]["applicationDatabase"]["security"] = {
        "certsSecretPrefix": appdb_cert_prefix,
        "tls": {"ca": appdb_ca_configmap},
    }
    return resource


def appdb_statefulset(external_appdb: MongoDBMulti, member_cluster_name: str) -> kubernetes.client.V1StatefulSet:
    cluster_spec_list = external_appdb["spec"]["clusterSpecList"]
    cluster_index = next(
        index for index, item in enumerate(cluster_spec_list) if item["clusterName"] == member_cluster_name
    )
    sts_name = f"{external_appdb.name}-{cluster_index}"
    return kubernetes.client.AppsV1Api(
        api_client=get_member_cluster_api_client(member_cluster_name)
    ).read_namespaced_stateful_set(sts_name, external_appdb.namespace)


def assert_multi_cluster_appdb_statefulset_identity(
    external_appdb: MongoDBMulti,
    member_cluster_names: list[str],
):
    expected_owner = f"{external_appdb.namespace}-{external_appdb.name}"
    for cluster_name in member_cluster_names:
        sts = appdb_statefulset(external_appdb, cluster_name)
        assert sts.metadata.labels.get("mongodbmulticluster") == expected_owner, (
            f"AppDB StatefulSet {sts.metadata.name} in cluster {cluster_name} must be owned by the "
            f"external AppDB {expected_owner}, but got labels: {sts.metadata.labels}"
        )

    assert_no_appdb_migration_annotations(external_appdb, member_cluster_names)


def assert_no_appdb_migration_annotations(external_appdb: MongoDBMulti, member_cluster_names: list[str]):
    for cluster_name in member_cluster_names:
        annotations = appdb_statefulset(external_appdb, cluster_name).metadata.annotations or {}
        assert "mongodb.com/appdb-migration-ready" not in annotations
        assert "mongodb.com/appdb-reverse-migration-ready" not in annotations


def read_appdb_connection_url(primary_om: MongoDBOpsManager, member_cluster_name: str) -> str:
    api_client = get_member_cluster_api_client(member_cluster_name) if primary_om.is_om_multi_cluster() else None
    secret = kubernetes.client.CoreV1Api(api_client=api_client).read_namespaced_secret(
        primary_om.get_appdb_connection_url_secret_name(), primary_om.namespace
    )
    return KubernetesTester.decode_secret(secret.data)["connectionString"]


def assert_no_internal_appdb_statefulset(namespace: str, appdb_name: str, api_client: kubernetes.client.ApiClient):
    kubernetes.client.AppsV1Api(api_client=api_client).read_namespaced_stateful_set(appdb_name, namespace)
