from kubetester.kubetester import KubernetesTester
from kubetester.operator import Operator
from pytest import mark


@mark.e2e_sharded_cluster_schema_validation
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterValidationMongosMissing(KubernetesTester):
    """
    name: Sharded Cluster Validation (mongos missing)
    create:
      file: sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/mongosCount"}]'
      exception: 'Unprocessable Entity'
    """

    @mark.skip
    def test_validation_ok(self):
        assert True


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterValidationShardCountMissing(KubernetesTester):
    """
    name: Sharded Cluster Validation (shardCount missing)
    create:
      file: sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/shardCount"}]'
      exception: 'Unprocessable Entity'
    """

    @mark.skip
    def test_validation_ok(self):
        assert True


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterValidationConfigServerCount(KubernetesTester):
    """
    name: Sharded Cluster Validation (configServerCount missing)
    create:
      file: sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/configServerCount"}]'
      exception: 'Unprocessable Entity'
    """

    @mark.skip
    def test_validation_ok(self):
        assert True


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterValidationMongoDsPerShard(KubernetesTester):
    """
    name: Sharded Cluster Validation (mongodsPerShardCount missing)
    create:
      file: sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/mongodsPerShardCount"}]'
      exception: 'Unprocessable Entity'
    """

    @mark.skip
    def test_validation_ok(self):
        assert True


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterValidationInvalidType(KubernetesTester):
    """
    name: Sharded Cluster Validation (invalid type)
    create:
      file: sharded-cluster.yaml
      patch: '[{"op":"replace","path":"/spec/type","value":"InvalidShardedClusterType"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterValidationInvalidLogLevel(KubernetesTester):
    """
    name: Sharded Cluster Validation (invalid logLevel)
    create:
      file: sharded-cluster.yaml
      patch: '[{"op":"replace","path":"/spec/logLevel","value":"NotDEBUG"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterSchemaAdditionalMongodConfigNotAllowed(KubernetesTester):
    """
    name: Sharded Cluster Validation (additional Mongod Config not allowed)
    create:
      file: sharded-cluster.yaml
      patch: '[{"op":"add","path":"/spec/additionalMongodConfig","value":{"net":{"ssl":{"mode": "disabled"}}}}]'
      exception: 'cannot be specified if type of MongoDB is ShardedCluster'
    """

    def test_validation_ok(self):
        assert True


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterInvalidWithProjectAndOpsManager(KubernetesTester):
    init = {
        "create": {
            "file": "sharded-cluster.yaml",
            "patch": [
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


@mark.e2e_sharded_cluster_schema_validation
class TestShardedClusterInvalidWithCloudAndOpsManagerAndProject(KubernetesTester):
    init = {
        "create": {
            "file": "sharded-cluster.yaml",
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
