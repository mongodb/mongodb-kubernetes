from typing import Optional

from kubetester import create_or_update_secret, try_load
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.common.constants import S3_BLOCKSTORE_NAME, S3_OPLOG_NAME
from tests.conftest import AWS_REGION, is_multi_cluster
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

"""
This test checks the work with TLS-enabled backing databases (oplog & blockstore)
"""

S3_TEST_CA1 = "s3-test-ca-1"
S3_TEST_CA2 = "s3-test-ca-2"
S3_NOT_WORKING_CA = "not-working-ca"


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    return create_appdb_certs(namespace, issuer, "om-backup-tls-s3-db")


@fixture(scope="module")
def s3_bucket_oplog(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_OPLOG_NAME + "-secret", namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-oplog-")


@fixture(scope="module")
def s3_bucket_blockstore(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_BLOCKSTORE_NAME + "-secret", namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-blockstorage-")


@fixture(scope="module")
def duplicate_configmap_ca(namespace, amazon_ca_1_filepath, amazon_ca_2_filepath, ca_path):
    ca = open(amazon_ca_1_filepath).read()
    data = {"ca-pem": ca}
    create_or_update_secret(namespace, S3_TEST_CA1, data)

    ca = open(amazon_ca_2_filepath).read()
    data = {"ca-pem": ca}
    create_or_update_secret(namespace, S3_TEST_CA2, data)

    ca = open(ca_path).read()
    data = {"ca-pem": ca}
    create_or_update_secret(namespace, S3_NOT_WORKING_CA, data)


@fixture(scope="module")
def ops_manager(
    namespace,
    duplicate_configmap_ca,
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

    if try_load(resource):
        return resource

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()

    custom_certificate = {"name": S3_NOT_WORKING_CA, "key": "ca-pem"}

    resource["spec"]["backup"]["s3Stores"][0]["name"] = S3_BLOCKSTORE_NAME
    resource["spec"]["backup"]["s3Stores"][0]["s3SecretRef"]["name"] = S3_BLOCKSTORE_NAME + "-secret"
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket_blockstore
    resource["spec"]["backup"]["s3Stores"][0]["s3RegionOverride"] = AWS_REGION
    resource["spec"]["backup"]["s3Stores"][0]["customCertificateSecretRefs"] = [custom_certificate]
    resource["spec"]["backup"]["s3OpLogStores"][0]["name"] = S3_OPLOG_NAME
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3SecretRef"]["name"] = S3_OPLOG_NAME + "-secret"
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketName"] = s3_bucket_oplog
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3RegionOverride"] = AWS_REGION

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource["spec"]["configuration"]["brs.legacy.cert.behavior"] = "true"

    return resource


@mark.e2e_om_ops_manager_backup_s3_tls
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_om_backup_is_failed(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            timeout=600,
            msg_regexp=".* valid certification path to requested target.*",
        )

    def test_om_with_correct_custom_cert(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        custom_certificate = [
            {"name": S3_TEST_CA1, "key": "ca-pem"},
            {"name": S3_TEST_CA2, "key": "ca-pem"},
        ]

        ops_manager["spec"]["backup"]["s3OpLogStores"][0]["customCertificateSecretRefs"] = custom_certificate
        ops_manager["spec"]["backup"]["s3Stores"][0]["customCertificateSecretRefs"] = custom_certificate

        ops_manager.update()

    def test_om_is_running(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        # this takes more time, since the change of the custom certs requires a restart of om
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=1200, ignore_errors=True)
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()

    def test_om_s3_stores(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_s3_stores([{"id": S3_BLOCKSTORE_NAME, "s3RegionOverride": AWS_REGION}])
        om_tester.assert_oplog_s3_stores([{"id": S3_OPLOG_NAME, "s3RegionOverride": AWS_REGION}])

        # verify that we were able to setup (and no error) certificates
        a = om_tester.get_s3_stores()
        assert a["results"][0]["customCertificates"][0]["filename"] == f"{S3_TEST_CA1}/ca-pem"
        assert a["results"][0]["customCertificates"][1]["filename"] == f"{S3_TEST_CA2}/ca-pem"
        b = om_tester.get_oplog_s3_stores()
        assert b["results"][0]["customCertificates"][0]["filename"] == f"{S3_TEST_CA1}/ca-pem"
        assert b["results"][0]["customCertificates"][1]["filename"] == f"{S3_TEST_CA2}/ca-pem"
