from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="class")
def replica_set(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace, name="replica-set")
    resource.configure(None, "replica-set")
    resource.set_version(custom_mdb_version)
    resource.create()
    return resource


@fixture(scope="class")
def replica_set_single(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-single.yaml"), namespace=namespace, name="replica-set-single"
    )
    resource.configure(None, "replica-set")
    resource.set_version(custom_mdb_version)
    resource.create()
    return resource


@fixture(scope="class")
def sharded_cluster(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster.yaml"), namespace=namespace, name="sharded-cluster")
    resource.configure(None, "sharded-cluster")
    resource.set_version(custom_mdb_version)
    resource.create()
    return resource


@fixture(scope="class")
def sharded_cluster_single(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-single.yaml"), namespace=namespace, name="sharded-cluster-single"
    )
    resource.configure(None, "sharded-cluster")
    resource.set_version(custom_mdb_version)
    resource.create()
    return resource


@mark.e2e_multiple_cluster_failures
class TestNoTwoReplicaSetsCanBeCreatedOnTheSameProject:
    def test_replica_set_get_to_running_state(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running)

        assert "warnings" not in replica_set["status"]

    def test_no_warnings_when_scaling(self, replica_set: MongoDB):
        replica_set["spec"]["members"] = 5
        replica_set.update()

        replica_set.assert_reaches_phase(Phase.Running, timeout=500)
        assert "warnings" not in replica_set["status"]

    def test_second_mdb_resource_fails(self, replica_set_single: MongoDB):
        replica_set_single.assert_reaches_phase(Phase.Pending)

        assert (
            replica_set_single["status"]["message"]
            == "Cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)"
        )

        assert "warnings" not in replica_set_single["status"]

    # pylint: disable=unused-argument
    def test_automation_config_is_correct(self, replica_set, replica_set_single):
        config = KubernetesTester.get_automation_config()

        assert len(config["processes"]) == 5
        for process in config["processes"]:
            assert not process["name"].startswith("my-replica-set-single")

        assert len(config["sharding"]) == 0
        assert len(config["replicaSets"]) == 1


@mark.e2e_multiple_cluster_failures
class TestNoTwoClustersCanBeCreatedOnTheSameProject:
    def test_sharded_cluster_reaches_running_phase(self, sharded_cluster: MongoDB):
        # Unfortunately, Sharded cluster takes a long time to even start.
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

        assert "warnings" not in sharded_cluster["status"]

    def test_second_mdb_sharded_cluster_fails(self, sharded_cluster_single: MongoDB):
        sharded_cluster_single.assert_reaches_phase(Phase.Pending)

        assert "warnings" not in sharded_cluster_single["status"]

    # pylint: disable=unused-argument
    def test_sharded_cluster_automation_config_is_correct(self, sharded_cluster, sharded_cluster_single):
        config = KubernetesTester.get_automation_config()

        for process in config["processes"]:
            assert not process["name"].startswith("sh001-single")

        # Creating a Sharded Cluster will result in 1 entry added to `sharding` and ...
        assert len(config["sharding"]) == 1
        # ... and also 2 entries into `replicaSets` (1 shard and 1 config replica set, in this case)
        assert len(config["replicaSets"]) == 2


@mark.e2e_multiple_cluster_failures
class TestNoTwoDifferentTypeOfResourceCanBeCreatedOnTheSameProject:
    def test_multiple_test_different_type_fails(self, replica_set_single: MongoDB):
        replica_set_single.assert_reaches_phase(Phase.Running)

    def test_adding_sharded_cluster_fails(self, sharded_cluster_single: MongoDB):
        sharded_cluster_single.assert_reaches_phase(Phase.Pending)

        status = sharded_cluster_single["status"]
        assert status["phase"] == "Pending"
        assert (
            status["message"]
            == "Cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)"
        )

        assert "warnings" not in status

    # pylint: disable=unused-argument
    def test_automation_config_contains_one_cluster(self, replica_set_single, sharded_cluster_single):
        config = KubernetesTester.get_automation_config()

        assert len(config["processes"]) == 1

        for process in config["processes"]:
            assert not process["name"].startswith("sh001-single")

        assert len(config["sharding"]) == 0
        assert len(config["replicaSets"]) == 1
