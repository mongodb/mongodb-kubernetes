from typing import Optional

import pytest
from kubernetes.client import ApiException
from kubetester import wait_for_webhook
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager

# This test still uses KubernetesTester therefore it wasn't used for Multi-Cluster AppDB tests.
# It was also moved to separate directory to clearly indicate this test is unmanageable until converting to normal imperative test.


def om_resource(namespace: str) -> MongoDBOpsManager:
    return MongoDBOpsManager.from_yaml(yaml_fixture("om_validation.yaml"), namespace=namespace)


@pytest.mark.e2e_om_appdb_validation
def test_wait_for_webhook(namespace: str):
    wait_for_webhook(namespace)


@pytest.mark.e2e_om_appdb_validation
def test_wrong_appdb_version(namespace: str, custom_version: Optional[str]):
    om = om_resource(namespace)
    om["spec"]["applicationDatabase"]["version"] = "3.6.12"
    om.set_version(custom_version)
    with pytest.raises(
        ApiException,
        match=r"the version of Application Database must be .* 4.0",
    ):
        om.create()


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerAppDbWrongSize(KubernetesTester):
    """
    name: Wrong size of AppDb
    description: |
      AppDB with members < 3 is not allowed
    create:
      file: om_validation.yaml
      patch: '[{"op":"replace","path":"/spec/applicationDatabase/members","value":2}]'
      exception: 'spec.applicationDatabase.members in body should be greater than or equal to 3'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerBackupEnabledNotSpecified(KubernetesTester):
    """
    name: Backup 'enabled' check
    description: |
      Backup specified but 'enabled' field is missing - it is required
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup","value":{}}]'
      exception: 'spec.backup.enabled in body is required'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerBackupOplogStoreNameRequired(KubernetesTester):
    """
    name: Backup 'enabled' check
    description: |
      Backup oplog store specified but missing 'name' field
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup","value":{"enabled": true, "opLogStores": [{"mongodbResourceRef": {}}]}}]'
      exception: 'spec.backup.opLogStores[0].name: Required value'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerBackupOplogStoreMongodbRefRequired(KubernetesTester):
    """
    name: Backup 'enabled' check
    description: |
      Backup oplog store specified but missing 'mongodbResourceRef' field
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup","value":{"enabled": true, "opLogStores": [{"name": "foo"}]}}]'
      exception: 'spec.backup.opLogStores[0].mongodbResourceRef: Required value'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerS3StoreNameRequired(KubernetesTester):
    """
    description: |
      S3 store specified but missing 'name' field
    create:
      file: om_s3store_validation.yaml
      patch: '[ { "op":"add","path":"/spec/backup/s3Stores/-","value": {} }]'
      exception: 'spec.backup.s3Stores[0].name: Required value'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerS3StorePathStyleAccessEnabledRequired(KubernetesTester):
    """
    description: |
      S3 store specified but missing 'pathStyleAccessEnabled' field
    create:
      file: om_s3store_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup/s3Stores/-","value":{ "name": "foo", "mongodbResourceRef": {"name":"my-rs" }}}]'
      exception: 'spec.backup.s3Stores[0].pathStyleAccessEnabled: Required value'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerS3StoreS3BucketEndpointRequired(KubernetesTester):
    """
    description: |
      S3 store specified but missing 's3BucketEndpoint' field
    create:
      file: om_s3store_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup/s3Stores/-","value":{ "name": "foo", "mongodbResourceRef": {"name":"my-rs" }, "pathStyleAccessEnabled": true }}]'
      exception: 'spec.backup.s3Stores[0].s3BucketEndpoint: Required value'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerS3StoreS3BucketNameRequired(KubernetesTester):
    """
    description: |
      S3 store specified but missing 's3BucketName' field
    create:
      file: om_s3store_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup/s3Stores/-","value":{ "name": "foo", "mongodbResourceRef": {"name":"my-rs" }, "pathStyleAccessEnabled": true , "s3BucketEndpoint": "my-endpoint"}}]'
      exception: 'spec.backup.s3Stores[0].s3BucketName: Required value'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerExternalConnectivityTypeRequired(KubernetesTester):
    """
    description: |
        'spec.externalConnectivity.type' is a required field
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/externalConnectivity","value":{}}]'
      exception: 'spec.externalConnectivity.type: Required value'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerExternalConnectivityWrongType(KubernetesTester):
    """
    description: |
        'spec.externalConnectivity.type' must be either "LoadBalancer" or "NodePort"
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/externalConnectivity","value":{"type": "nginx"}}]'
      exception: 'spec.externalConnectivity.type in body should be one of [LoadBalancer NodePort]'
    """

    def test_validation_ok(self):
        assert True
