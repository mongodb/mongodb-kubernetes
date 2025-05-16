import time

import pytest
from kubernetes.client import ApiException
from kubetester import MongoDB
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture


def mdb_resource(namespace: str) -> MongoDB:
    return MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)


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

    @pytest.mark.skip(reason="TODO: validation for members must be done in webhook depending on the resource type")
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
class TestReplicaSetSchemaShardedClusterMongodConfig(KubernetesTester):
    """
    name: Replica Set Validation (sharded cluster additional mongod config)
    create:
      file: replica-set.yaml
      patch: '[{"op":"add","path":"/spec/mongos","value":{"additionalMongodConfig":{"net":{"ssl":{"mode": "AllowSSL"}}}}}]'
      exception: 'cannot be specified if type of MongoDB is ReplicaSet'
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
class TestReplicaSetInvalidWithCloudAndOpsManagerAndProject(KubernetesTester):
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
def test_resource_type_immutable(namespace: str):
    mdb = mdb_resource(namespace).create()
    # no need to wait for the resource get to Running state - we can update right away
    time.sleep(5)
    mdb = mdb_resource(namespace).load()

    with pytest.raises(
        ApiException,
        match=r"'resourceType' cannot be changed once created",
    ):
        mdb["spec"]["type"] = "ShardedCluster"
        # Setting mandatory fields to avoid getting another validation error
        mdb["spec"]["shardCount"] = 1
        mdb["spec"]["mongodsPerShardCount"] = 3
        mdb["spec"]["mongosCount"] = 1
        mdb["spec"]["configServerCount"] = 1
        mdb.update()


@pytest.mark.e2e_replica_set_schema_validation
def test_unrecognized_auth_mode(namespace: str):
    mdb = MongoDB.from_yaml(yaml_fixture("invalid_replica_set_wrong_auth_mode.yaml"), namespace=namespace)

    with pytest.raises(
        ApiException,
        match=r".*Unsupported value.*SHA.*supported values.*X509.*SCRAM.*SCRAM-SHA-1.*MONGODB-CR.*SCRAM-SHA-256.*LDAP.*",
    ):
        mdb.create()
