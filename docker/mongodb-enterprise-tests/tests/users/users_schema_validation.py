import pytest

from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_users_schema_validation
class TestUsersSchemaValidationDbNotExternal(KubernetesTester):
    """
    name: Validation for mongodbusers (db)
    create:
      file: user_with_roles.yaml
      patch: '[{"op":"replace","path":"/spec/db", "value":"BadValue"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_users_schema_validation
class TestUsersSchemaValidationUserName(KubernetesTester):
    """
    name: Validation for mongodbusers (username)
    create:
      file: user_with_roles.yaml
      patch: '[{"op":"remove","path":"/spec/username"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_users_schema_validation
class TestUsersSchemaValidationRoleName(KubernetesTester):
    """
    name: Validation for mongodbusers (role name)
    create:
      file: user_with_roles.yaml
      patch: '[{"op":"remove","path":"/spec/roles/0/name"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_users_schema_validation
class TestUsersSchemaValidationRoleDb(KubernetesTester):
    """
    name: Validation for mongodbusers (role db)
    create:
      file: user_with_roles.yaml
      patch: '[{"op":"remove","path":"/spec/roles/0/db"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True
