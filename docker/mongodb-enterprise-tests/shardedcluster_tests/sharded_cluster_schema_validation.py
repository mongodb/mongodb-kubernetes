from kubetester import KubernetesTester

class TestShardedClusterValidationMongosMissing(KubernetesTester):
    """
    name: Sharded Cluster Validation (mongos missing)
    create:
      file: fixtures/sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/mongosCount"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True


class TestShardedClusterValidationShardCountMissing(KubernetesTester):
    """
    name: Sharded Cluster Validation (shardCount missing)
    create:
      file: fixtures/sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/shardCount"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True

class TestShardedClusterValidationConfigServerCount(KubernetesTester):
    """
    name: Sharded Cluster Validation (configServerCount missing)
    create:
      file: fixtures/sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/configServerCount"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True

class TestShardedClusterValidationMongoDsPerShard(KubernetesTester):
    """
    name: Sharded Cluster Validation (mongodsPerShardCount missing)
    create:
      file: fixtures/sharded-cluster.yaml
      patch: '[{"op":"remove","path":"/spec/mongodsPerShardCount"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True

class TestShardedClusterValidationInvalidType(KubernetesTester):
    """
    name: Sharded Cluster Validation (invalid type)
    create:
      file: fixtures/sharded-cluster.yaml
      patch: '[{"op":"replace","path":"/spec/type","value":"InvalidShardedClusterType"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True
