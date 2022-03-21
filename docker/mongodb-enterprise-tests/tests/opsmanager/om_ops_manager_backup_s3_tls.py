from typing import Optional

from pytest import mark, fixture

from kubetester import MongoDB
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from tests.opsmanager.conftest import ensure_ent_version
from tests.opsmanager.om_ops_manager_backup import (
    AWS_REGION,
    create_aws_secret,
    create_s3_bucket,
)
from tests.opsmanager.om_ops_manager_https import create_mongodb_tls_certs

"""
This test checks the work with TLS-enabled backing databases (oplog & blockstore)
"""

S3_OPLOG_NAME = "s3-oplog"
S3_BLOCKSTORE_NAME = "s3-blockstore"


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer, namespace, "om-backup-tls-s3-db", "appdb-om-backup-tls-s3-db-cert"
    )
    return "appdb"


@fixture(scope="module")
def s3_bucket_oplog(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_OPLOG_NAME + "-secret", namespace)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def oplog_certs_secret(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer, namespace, OPLOG_RS_NAME, f"oplog-{OPLOG_RS_NAME}-cert"
    )
    return "oplog"


@fixture(scope="module")
def s3_bucket_blockstore(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_BLOCKSTORE_NAME + "-secret", namespace)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def ops_manager(
    namespace,
    issuer_ca_configmap: str,
    appdb_certs_secret: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    s3_bucket_oplog: str,
    s3_bucket_blockstore: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_tls_s3.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()

    resource["spec"]["backup"]["s3Stores"][0]["name"] = S3_BLOCKSTORE_NAME
    resource["spec"]["backup"]["s3Stores"][0]["s3SecretRef"]["name"] = (
        S3_BLOCKSTORE_NAME + "-secret"
    )
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketEndpoint"] = s3_endpoint(
        AWS_REGION
    )
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket_blockstore
    resource["spec"]["backup"]["s3OpLogStores"][0]["name"] = S3_OPLOG_NAME
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3SecretRef"]["name"] = (
        S3_OPLOG_NAME + "-secret"
    )
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketEndpoint"] = s3_endpoint(
        AWS_REGION
    )
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketName"] = s3_bucket_oplog

    return resource.create()


@mark.e2e_om_ops_manager_backup_s3_tls
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)

        # appdb rolling restart for configuring monitoring
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)

    def test_om_is_running(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running, timeout=600, ignore_errors=True
        )
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
