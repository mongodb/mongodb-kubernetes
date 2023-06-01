from typing import List

import kubernetes
import kubernetes.client
from pytest import mark, fixture
from kubetester.awss3client import AwsS3Client, s3_endpoint

from kubetester import (
    create_or_update,
    create_or_update_configmap,
)
from kubetester import try_load

from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import (
    fixture as yaml_fixture,
)
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager

from .conftest import cluster_spec_list

from tests.opsmanager.om_ops_manager_backup import (
    AWS_REGION,
    create_aws_secret,
    create_s3_bucket,
)

TEST_DATA = {"name": "John", "address": "Highway 37", "age": 30}
MONGODB_PORT = 30000

S3_OPLOG_NAME = "s3-oplog"
S3_BLOCKSTORE_NAME = "s3-blockstore"
USER_PASSWORD = "/qwerty@!#:"


@fixture(scope="module")
def s3_bucket_oplog(
    aws_s3_client: AwsS3Client,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    create_aws_secret(aws_s3_client, S3_OPLOG_NAME + "-secret", namespace, central_cluster_client)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def s3_bucket_blockstore(
    aws_s3_client: AwsS3Client,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    create_aws_secret(aws_s3_client, S3_BLOCKSTORE_NAME + "-secret", namespace, central_cluster_client)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def ops_manager_certs(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_ops_manager_tls_certs(
        multi_cluster_issuer,
        namespace,
        "om-backup",
        secret_name="mdb-om-backup-cert",
        # We need the interconnected certificate since we update coreDNS later with that ip -> domain
        # because our central cluster is not part of the mesh, but we can access the pods via external IPs.
        # Since we are using TLS we need a certificate for a hostname, an IP does not work, hence
        #  f"om-backup.{namespace}.interconnected" -> IP setup below
        additional_domains=["fastdl.mongodb.org", f"om-backup.{namespace}.interconnected"],
        api_client=central_cluster_client,
    )


def create_project_config_map(om: MongoDBOpsManager, mdb_name, project_name, client, custom_ca):
    name = f"{mdb_name}-config"
    data = {
        "baseUrl": om.om_status().get_url(),
        "projectName": project_name,
        "sslMMSCAConfigMap": custom_ca,
        "orgId": "",
    }

    create_or_update_configmap(om.namespace, name, data, client)


@fixture(scope="module")
def multi_cluster_s3_replica_set(
    ops_manager,
    namespace,
    member_cluster_names: List[str],
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi-cluster.yaml"), "multi-replica-set", namespace
    ).configure(ops_manager, "s3metadata", api_client=central_cluster_client)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield create_or_update(resource)


@fixture(scope="module")
def ops_manager(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    custom_appdb_version: str,
    ops_manager_certs: str,
    s3_bucket_oplog: str,
    s3_bucket_blockstore: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:

    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_tls_s3.yaml"), namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    # resource["spec"]["externalConnectivity"] = {"type": "LoadBalancer"}

    resource.allow_mdb_rc_versions()

    del resource["spec"]["security"]
    del resource["spec"]["applicationDatabase"]["security"]

    # configure S3 Blockstore
    resource["spec"]["backup"]["s3Stores"][0]["name"] = S3_BLOCKSTORE_NAME
    resource["spec"]["backup"]["s3Stores"][0]["s3SecretRef"]["name"] = S3_BLOCKSTORE_NAME + "-secret"
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket_blockstore
    resource["spec"]["backup"]["s3Stores"][0]["s3RegionOverride"] = AWS_REGION

    # configure S3 Oplog
    resource["spec"]["backup"]["s3OpLogStores"][0]["name"] = S3_OPLOG_NAME
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3SecretRef"]["name"] = S3_OPLOG_NAME + "-secret"
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketName"] = s3_bucket_oplog
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3RegionOverride"] = AWS_REGION

    resource.create_admin_secret(api_client=central_cluster_client)

    try_load(resource)
    return resource


@mark.e2e_multi_cluster_s3_based_backup_restore
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_s3_based_backup_restore
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled.
    """

    def test_create_om(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager["spec"]["backup"]["members"] = 1
        create_or_update(ops_manager)

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)

    def test_om_is_running(self, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient):
        # at this point AppDB is used as the "metadatastore"
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=1000, ignore_errors=True)
        om_tester = ops_manager.get_om_tester(api_client=central_cluster_client)
        om_tester.assert_healthiness()

    def test_add_metadatastore(
        self,
        multi_cluster_s3_replica_set: MongoDBMulti,
        ops_manager: MongoDBOpsManager,
    ):
        multi_cluster_s3_replica_set.assert_reaches_phase(Phase.Running, timeout=800)

        # configure metadatastore in om, use dedicate MDB instead of AppDB
        ops_manager.load()
        ops_manager["spec"]["backup"]["s3Stores"][0]["mongodbResourceRef"] = {"name": multi_cluster_s3_replica_set.name}
        ops_manager["spec"]["backup"]["s3OpLogStores"][0]["mongodbResourceRef"] = {
            "name": multi_cluster_s3_replica_set.name
        }
        ops_manager.update()

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=10000)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=1000, ignore_errors=True)

    def test_om_s3_stores(self, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient):
        om_tester = ops_manager.get_om_tester(api_client=central_cluster_client)
        om_tester.assert_s3_stores([{"id": S3_BLOCKSTORE_NAME, "s3RegionOverride": AWS_REGION}])
        om_tester.assert_oplog_s3_stores([{"id": S3_OPLOG_NAME, "s3RegionOverride": AWS_REGION}])
