import pytest
from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetMembersMissing(KubernetesTester):
    """
    name: Replica Set Validation (members missing)
    tags: replica-set
    description: |
      Creates a Replica Set with required fields missing.
    create:
      file: replica-set.yaml
      patch: '[{"op":"remove","path":"/spec/members"}]'
      exception: 'Unprocessable Entity'
    """

    @pytest.mark.skip
    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetInvalidType(KubernetesTester):
    """
    name: Replica Set Validation (invalid type)
    tags: replica-set
    description: |
      Creates a Replica Set with an invalid field.
    create:
      file: replica-set.yaml
      patch: '[{"op":"replace","path":"/spec/type","value":"InvalidReplicaSetType"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetInvalidLogLevel(KubernetesTester):
    """
    name: Replica Set Validation (invalid type)
    tags: replica-set
    description: |
      Creates a Replica Set with an invalid logLevel.
    create:
      file: replica-set.yaml
      patch: '[{"op":"replace","path":"/spec/logLevel","value":"NotWARNING"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetSchemaInvalidSSL(KubernetesTester):
    """
    name: Replica Set Validation (invalid ssl mode)
    create:
      file: standalone.yaml
      patch: '[{"op":"add","path":"/spec","value":{"additionalMongodConfig":{"net":{"ssl":{"mode": "AllowDisallowSSL"}}}}}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@pytest.mark.skip(reason="OpenAPIv2 does not support this validation.")
@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetInvalidWithOpsAndCloudManager(KubernetesTester):
    """Creates a Replica Set with both a cloud manager and an ops manager
    configuration specified."""

    init = {
        "create": {
            "file": "replica-set.yaml",
            "patch": [
                {
                    "op": "add",
                    "path": "/spec/cloudManager",
                    "value": {"configMapRef": {"name": "something"}},
                },
                {
                    "op": "add",
                    "path": "/spec/opsManager",
                    "value": {"configMapRef": {"name": "something"}},
                },
            ],
            "exception": "must validate one and only one schema",
        },
    }

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetInvalidWithProjectAndCloudManager(KubernetesTester):
    """Creates a Replica Set with both a cloud manager and an ops manager
    configuration specified."""

    init = {
        "create": {
            "file": "replica-set.yaml",
            "patch": [
                {"op": "add", "path": "/spec/project", "value": {"name": "something"}},
                {
                    "op": "add",
                    "path": "/spec/cloudManager",
                    "value": {"configMapRef": {"name": "something"}},
                },
            ],
            "exception": "must validate one and only one schema",
        },
    }

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetInvalidWithProjectAndOpsManager(KubernetesTester):
    init = {
        "create": {
            "file": "replica-set.yaml",
            "patch": [
                {"op": "add", "path": "/spec/project", "value": {"name": "something"}},
                {
                    "op": "add",
                    "path": "/spec/opsManager",
                    "value": {"configMapRef": {"name": "something"}},
                },
            ],
            "exception": "must validate one and only one schema",
        },
    }

    def test_validation_ok(self):
        assert True


@pytest.mark.e2e_replica_set_schema_validation
class TestReplicaSetInvalidWithCloudAndOpsManagerAndProject(KubernetesTester):
    init = {
        "create": {
            "file": "replica-set.yaml",
            "patch": [
                {"op": "add", "path": "/spec/project", "value": {"name": "something"}},
                {
                    "op": "add",
                    "path": "/spec/cloudManager",
                    "value": {"configMapRef": {"name": "something"}},
                },
                {
                    "op": "add",
                    "path": "/spec/opsManager",
                    "value": {"configMapRef": {"name": "something"}},
                },
            ],
            "exception": "must validate one and only one schema",
        },
    }

    def test_validation_ok(self):
        assert True
