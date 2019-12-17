import pytest
from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneSchemaCredentialsMissing(KubernetesTester):
    """
    name: Validation for standalone (credentials missing)
    create:
      file: standalone.yaml
      patch: '[{"op":"remove","path":"/spec/credentials"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneSchemaProjectMissing(KubernetesTester):
    """
    name: Validation for standalone (project missing)
    create:
      file: standalone.yaml
      patch: '[{"op":"remove","path":"/spec/project"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneSchemaVersionMissing(KubernetesTester):
    """
    name: Validation for standalone (version missing)
    create:
      file: standalone.yaml
      patch: '[{"op":"remove","path":"/spec/version"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneSchemaInvalidType(KubernetesTester):
    """
    name: Validation for standalone (invalid type)
    create:
      file: standalone.yaml
      patch: '[{"op":"replace","path":"/spec/type","value":"InvalidStandaloneType"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneSchemaInvalidLogLevel(KubernetesTester):
    """
    name: Validation for standalone (invalid logLevel)
    create:
      file: standalone.yaml
      patch: '[{"op":"replace","path":"/spec/logLevel","value":"NotINFO"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneSchemaInvalidSSL(KubernetesTester):
    """
    name: Validation for standalone (invalid ssl mode)
    create:
      file: standalone.yaml
      patch: '[{"op":"add","path":"/spec","value":{"additionalMongodConfig":{"net":{"ssl":{"mode": "AllowDisallowSSL"}}}}}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneInvalidWithProjectAndCloudManager(KubernetesTester):
    init = {
        "create": {
            "file": "standalone.yaml",
            "patch": [
                {"op": "add", "path": "/spec/project", "value": "something"},
                {
                    "op": "add",
                    "path": "/spec/cloudManager",
                    "value": {"configMapRef": "something"},
                },
            ],
            "exception": "must validate one and only one schema",
        },
    }

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneInvalidWithProjectAndOpsManager(KubernetesTester):
    init = {
        "create": {
            "file": "standalone.yaml",
            "patch": [
                {"op": "add", "path": "/spec/project", "value": "something"},
                {
                    "op": "add",
                    "path": "/spec/opsManager",
                    "value": {"configMapRef": "something"},
                },
            ],
            "exception": "must validate one and only one schema",
        },
    }

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_standalone_schema_validation
class TestStandaloneInvalidWithCloudAndOpsManagerAndProject(KubernetesTester):
    init = {
        "create": {
            "file": "standalone.yaml",
            "patch": [
                {"op": "add", "path": "/spec/project", "value": "something"},
                {
                    "op": "add",
                    "path": "/spec/cloudManager",
                    "value": {"configMapRef": "something"},
                },
                {
                    "op": "add",
                    "path": "/spec/opsManager",
                    "value": {"configMapRef": "something"},
                },
            ],
            "exception": "must validate one and only one schema",
        },
    }

    def test_validation_ok(self):
        assert True
