import threading
import time

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
            threads.append(threading.Thread(target=cls.create_custom_resource_from_file, args=args))

        [t.start() for t in threads]
        [t.join() for t in threads]

        print("Waiting until any of the resources gets to 'Running' state..")
        run_periodically(TestRaceConditions.any_resource_created, timeout=360)

    def test_one_resource_created_only(self):
        mongodbs = [
            KubernetesTester.get_namespaced_custom_object(KubernetesTester.get_namespace(), resource, "MongoDB")
            for resource in mdb_resources.keys()
        ]
        assert len([m for m in mongodbs if m["status"]["phase"] == "Running"]) == 1

        # No duplicated organizations were created and automation config is consistent
        organizations = KubernetesTester.find_organizations(KubernetesTester.get_om_group_name())
        assert len(organizations) == 1
        groups = KubernetesTester.find_groups_in_organization(organizations[0], KubernetesTester.get_om_group_name())
        assert len(groups) == 1

        config = KubernetesTester.get_automation_config()

        # making sure that only one single mdb resource was created
        replica_set_created = len(config["replicaSets"]) == 1 and len(config["processes"]) == 1
        sharded_cluster_created = len(config["replicaSets"]) == 2 and len(config["processes"]) == 3
        standalone_created = len(config["replicaSets"]) == 0 and len(config["processes"]) == 1

        assert replica_set_created + sharded_cluster_created + standalone_created == 1

    @staticmethod
    def any_resource_created():
        namespace = KubernetesTester.get_namespace()
        results = [
            KubernetesTester.check_phase(namespace, "MongoDB", resource, "Running") for resource in mdb_resources.keys()
        ]

        print("Standalone ready: {}, sharded cluster ready: {}, replica set ready: {}".format(*results))
        return any(results)
