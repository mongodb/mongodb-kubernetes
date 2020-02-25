import pytest

from tests.opsmanager.om_base import OpsManagerBase


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerAppDbWrongVersion(OpsManagerBase):
    """
    name: Wrong version of AppDB
    description: |
      AppDB with version < 4.0.0 are not allowed
    create:
      file: om_appdb_validation.yaml
      wait_until: om_in_error_state
      timeout: 100
    """

    def test_om_appdb_version_validation(self):
        assert "must be >= 4.0" in self.om_cr.get_om_status()["message"]


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerAppDbWrongSize(OpsManagerBase):
    """
    name: Wrong size of AppDb
    description: |
      AppDB with members < 3 is not allowed
    create:
      file: om_appdb_validation.yaml
      patch: '[{"op":"replace","path":"/spec/applicationDatabase/members","value":2}]'
      exception: 'spec.applicationDatabase.members in body should be greater than or equal to 3'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerBackupEnabledNotSpecified(OpsManagerBase):
    """
    name: Backup 'enabled' check
    description: |
      Backup specified but 'enabled' field is missing - it is required
    create:
      file: om_appdb_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup","value":{}}]'
      exception: 'spec.backup.enabled in body is required'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerBackupOplogStoreNameRequired(OpsManagerBase):
    """
    name: Backup 'enabled' check
    description: |
      Backup oplog store specified but missing 'name' field
    create:
      file: om_appdb_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup","value":{"enabled": true, "opLogStores": [{"mongodbResourceRef": {}}]}}]'
      exception: 'spec.backup.opLogStores.name in body is required'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerBackupOplogStoreMongodbRefRequired(OpsManagerBase):
    """
    name: Backup 'enabled' check
    description: |
      Backup oplog store specified but missing 'mongodbResourceRef' field
    create:
      file: om_appdb_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup","value":{"enabled": true, "opLogStores": [{"name": "foo"}]}}]'
      exception: 'spec.backup.opLogStores.mongodbResourceRef in body is required'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerS3StoreNameRequired(OpsManagerBase):
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
class TestOpsManagerS3StoreMongoDBResourceRefRequired(OpsManagerBase):
    """
    description: |
      S3 store specified but missing 'mongodbResourceRef' field
    create:
      file: om_s3store_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup/s3Stores/-", "value": { "name": "foo" }}]'
      exception: 'spec.backup.s3Stores.mongodbResourceRef in body is required'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerS3StorePathStyleAccessEnabledRequired(OpsManagerBase):
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
class TestOpsManagerS3StoreS3BucketEndpointRequired(OpsManagerBase):
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
class TestOpsManagerS3StoreS3BucketNameRequired(OpsManagerBase):
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
class TestOpsManagerS3StoreS3SecretRequired(OpsManagerBase):
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
class TestOpsManagerExternalConnectivityTypeRequired(OpsManagerBase):
    """
    description: |
        'spec.externalConnectivity.type' is a required field
    create:
      file: om_appdb_validation.yaml
      patch: '[{"op":"add","path":"/spec/externalConnectivity","value":{}}]'
      exception: 'spec.externalConnectivity.type in body is required'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_om_appdb_validation
class TestOpsManagerExternalConnectivityWrongType(OpsManagerBase):
    """
    description: |
        'spec.externalConnectivity.type' must be either "LoadBalancer" or "NodePort"
    create:
      file: om_appdb_validation.yaml
      patch: '[{"op":"add","path":"/spec/externalConnectivity","value":{"type": "nginx"}}]'
      exception: 'spec.externalConnectivity.type in body should be one of [LoadBalancer NodePort]'
    """

    def test_validation_ok(self):
        assert True
