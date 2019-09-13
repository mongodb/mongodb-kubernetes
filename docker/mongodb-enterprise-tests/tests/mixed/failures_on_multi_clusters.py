from kubetester.kubetester import KubernetesTester
import pytest


@pytest.mark.e2e_multiple_cluster_failures
class TestMultipleClustersHaveFailures0(KubernetesTester):
    """
    name: Creating multiple clusters should result on a failure on the second one
    create:
      file: replica-set.yaml
      wait_until: in_running_state
    """

    def test_replica_set_is_ready_no_warnings(self):
        mdb = KubernetesTester.get_resource()
        assert "warnings" not in mdb["status"]


@pytest.mark.e2e_multiple_cluster_failures
class TestMultipleClustersHaveFailures1(KubernetesTester):
    """
    name: Applying a change should not add a warning
    update:
      file: replica-set.yaml
      patch: '[{"op":"replace","path":"/spec/members","value":5}]'
      wait_until: in_running_state
    """

    def test_replica_set_is_ready_no_warnings_after_update(self):
        mdb = KubernetesTester.get_resource()
        assert "warnings" not in mdb["status"]


@pytest.mark.e2e_multiple_cluster_failures
class TestMultipleClustersHaveFailures2(KubernetesTester):
    """
    name: Creating a new cluster in the same project should fail
    create:
      file: replica-set-single.yaml
      wait_until: in_failed_state
    """

    def test_replica_set_failed(self):
        mdb = KubernetesTester.get_resource()
        assert mdb["status"]["message"] == "Cannot create more than 1 MongoDB Cluster per project"

        # No warnings in the failed resource
        assert "warnings" not in mdb["status"]

    def test_no_mention_of_new_processes_in_automation_config(self):
        config = self.get_automation_config()

        assert len(config["processes"]) == 5

        for process in config["processes"]:
            assert not process["name"].startswith("my-replica-set-single")

    def test_no_new_sharded_cluster_was_added(self):
        config = self.get_automation_config()

        assert len(config["sharding"]) == 0
        assert len(config["replicaSets"]) == 1

    @classmethod
    def teardown_env(cls):
        cls.delete_custom_resource(cls.get_namespace(), "my-replica-set", "MongoDB")
        cls.delete_custom_resource(cls.get_namespace(), "my-replica-set-single", "MongoDB")


@pytest.mark.e2e_multiple_cluster_failures
class TestMultipleClustersHaveFailures4(KubernetesTester):
    """
    name: Adding a new sharded cluster (after removal of old Rs) does not result in warnings
    create:
      file: sharded-cluster.yaml
      wait_until: in_running_state
    """

    def test_cluster_is_ready_no_warnings(self):
        mdb = KubernetesTester.get_resource()
        assert "warnings" not in mdb["status"]


@pytest.mark.e2e_multiple_cluster_failures
class TestMultipleClustersHaveFailures5(KubernetesTester):
    """
    name: Creating a new cluster should fail without warnings
    create:
      file: sharded-cluster-single.yaml
      wait_until: in_failed_state
    """

    def test_cluster_is_ready_warnings(self):
        mdb = KubernetesTester.get_resource()
        assert mdb["status"]["message"] == "Cannot create more than 1 MongoDB Cluster per project"

        assert "warnings" not in mdb["status"]

    def test_no_mention_of_new_processes_in_automation_config(self):
        config = self.get_automation_config()

        for process in config["processes"]:
            assert not process["name"].startswith("sh001-single")

    def test_no_new_sharded_cluster_was_added(self):
        config = self.get_automation_config()

        assert len(config["sharding"]) == 1

    @classmethod
    def teardown_env(cls):
        cls.delete_custom_resource(cls.get_namespace(), "sh001-base", "MongoDB")
        cls.delete_custom_resource(cls.get_namespace(), "sh001-single", "MongoDB")


@pytest.mark.e2e_multiple_cluster_failures
class TestMultipleClustersHaveFailures7(KubernetesTester):
    """
    name: Creating multiple clusters should result on a failure on the second one
    create:
      file: replica-set-single.yaml
      wait_until: in_running_state
    """

    def test_replica_set_is_ready_no_warnings(self):
        mdb = KubernetesTester.get_resource()
        assert "warnings" not in mdb["status"]


@pytest.mark.e2e_multiple_cluster_failures
class TestMultipleClustersHaveFailures8(KubernetesTester):
    """
    name: Creating a new cluster should fail without warnings
    create:
      file: sharded-cluster-single.yaml
      wait_until: in_failed_state
    """

    def test_cluster_is_ready_warnings(self):
        mdb = KubernetesTester.get_resource()
        assert mdb["status"]["message"] == "Cannot create more than 1 MongoDB Cluster per project"

        assert "warnings" not in mdb["status"]

    def test_no_mention_of_new_processes_in_automation_config(self):
        config = self.get_automation_config()

        assert len(config["processes"]) == 1

        for process in config["processes"]:
            assert not process["name"].startswith("sh001-single")

    def test_no_new_sharded_cluster_was_added(self):
        config = self.get_automation_config()

        assert len(config["sharding"]) == 0
        assert len(config["replicaSets"]) == 1

    @classmethod
    def teardown_env(cls):
        cls.delete_custom_resource(cls.get_namespace(), "my-replica-set-single", "MongoDB")
        cls.delete_custom_resource(cls.get_namespace(), "sh001-single", "MongoDB")
