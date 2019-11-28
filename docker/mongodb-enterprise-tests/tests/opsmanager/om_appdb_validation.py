import pytest

from tests.opsmanager.om_base import OpsManagerBase

gen_key_resource_version = None
admin_key_resource_version = None


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
    name: Wrong size of AppDB
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
