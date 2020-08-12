from typing import Optional

import pytest
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.kubetester import fixture as yaml_fixture
from pytest import fixture

from kubetester.mongodb import Phase

from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerAppDbWrongVersion:
    @classmethod
    @fixture(scope="class")
    def ops_manager(
        cls, namespace: str, custom_version: Optional[str]
    ) -> MongoDBOpsManager:
        """ The fixture for Ops Manager to be created."""
        om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
            yaml_fixture("om_validation.yaml"), namespace=namespace
        )
        om.set_version(custom_version)
        return om.create()

    def test_wrong_appdb_version(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(
            Phase.Failed,
            msg_regexp="The version of Application Database must be >= 4.0",
            timeout=20,
        )


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
      exception: 'spec.backup.opLogStores.name in body is required'
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
      exception: 'spec.backup.opLogStores.mongodbResourceRef in body is required'
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
      exception: 'spec.backup.s3Stores.name in body is required'
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
      exception: 'spec.backup.s3Stores.pathStyleAccessEnabled in body is required'
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
      exception: 'spec.backup.s3Stores.s3BucketEndpoint in body is required'
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
      exception: 'spec.backup.s3Stores.s3BucketName in body is required'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerS3StoreS3SecretRequired(KubernetesTester):
    """
    description: |
      S3 store specified but missing 's3SecretRef' field
    create:
      file: om_s3store_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup/s3Stores/-","value":{ "name": "foo", "mongodbResourceRef": {"name":"my-rs" }, "pathStyleAccessEnabled": true , "s3BucketEndpoint": "my-endpoint", "s3BucketName": "bucket-name"}}]'
      exception: 'spec.backup.s3Stores.s3SecretRef in body is required'
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
      exception: 'spec.externalConnectivity.type in body is required'
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
