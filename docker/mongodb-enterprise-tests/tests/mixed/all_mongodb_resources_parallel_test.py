import threading
import pytest

from kubetester.kubetester import KubernetesTester, fixture, run_periodically

mdb_resources = {
    "my-standalone": fixture("standalone.yaml"),
    "sh001-single": fixture("sharded-cluster-single.yaml"),
    "my-replica-set-single": fixture("replica-set-single.yaml"),
}


@pytest.mark.e2e_all_mongodb_resources_parallel
class TestRaceConditions(KubernetesTester):
    """
    name: Test for no race conditions during creation of three mongodb resources in parallel.
    description: |
        Makes sure no duplicated organizations/groups are created, while 3 mongodb resources
        are created in parallel. Also the automation config doesn't miss entries.
    """

    random_storage_name = None

    @classmethod
    def setup_env(cls):
        threads = []
        for filename in mdb_resources.values():
            args = (cls.get_namespace(), filename)
            threads.append(
                threading.Thread(target=cls.create_custom_resource_from_file, args=args)
            )

        [t.start() for t in threads]
        [t.join() for t in threads]

        print("Waiting until all three resources get 'Running' state..")
        run_periodically(TestRaceConditions.all_resources_created, timeout=360)

    def test_all_resources_created(self):
        # No duplicated organizations were created and automation config is consistent
        organizations = KubernetesTester.find_organizations(
            KubernetesTester.get_om_group_name()
        )
        assert len(organizations) == 1
        groups = KubernetesTester.find_groups_in_organization(
            organizations[0], KubernetesTester.get_om_group_name()
        )
        assert len(groups) == 1

        config = KubernetesTester.get_automation_config()

        # 2 replica sets for the sharded cluster and 1 - for the replica set
        assert len(config["replicaSets"]) == 3

        # 1 standalone, 1 rs, 1 sharded cluster (each replica set contains single members)
        assert len(config["processes"]) == 5

        assert len(config["sharding"]) == 1

    def test_all_resources_removed(self):
        threads = []
        for resource in mdb_resources.keys():
            args = (self.get_namespace(), resource, "MongoDB")
            threads.append(
                threading.Thread(
                    target=KubernetesTester.delete_custom_resource, args=args
                )
            )

        [t.start() for t in threads]
        [t.join() for t in threads]

        run_periodically(TestRaceConditions.all_resources_removed, timeout=360)

    @staticmethod
    def all_resources_created():
        namespace = KubernetesTester.get_namespace()
        results = [
            KubernetesTester.check_phase(namespace, "MongoDB", resource, "Running")
            for resource in mdb_resources.keys()
        ]

        print(
            "Standalone ready: {}, replica set ready: {}, sharded cluster ready: {}".format(
                *results
            )
        )
        return all(results)

    @staticmethod
    def all_resources_removed():
        results = [
            KubernetesTester.is_deleted(KubernetesTester.get_namespace(), r)
            for r in mdb_resources.keys()
        ]

        results.append(KubernetesTester.is_om_state_cleaned())
        print(
            "Standalone removed: {}, replica set removed: {}, sharded cluster removed: {}, Ops Manager cleaned: {}".format(
                *results
            )
        )

        return all(results)
