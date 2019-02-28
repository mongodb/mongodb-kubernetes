import threading

from kubetester import KubernetesTester, func_with_timeout, func_with_assertions


class TestRaceConditions(KubernetesTester):
    '''
    name: Test for no race conditions during creation of three mongodb resources in parallel
    description: |
        Makes sure no duplicated organizations/groups are created, also that the automation config doesn't miss entries

    '''
    random_storage_name = None

    @classmethod
    def setup_env(cls):
        # note, that all fixture files in the container are located in one 'fixtures' directory
        t1 = threading.Thread(target=cls.create_custom_resource_from_file,
                              args=(cls.get_namespace(), "fixtures/standalone.yaml"))
        t2 = threading.Thread(target=cls.create_custom_resource_from_file,
                              args=(cls.get_namespace(), "fixtures/sharded-cluster-single.yaml"))
        t3 = threading.Thread(target=cls.create_custom_resource_from_file,
                              args=(cls.get_namespace(), "fixtures/replica-set-single.yaml"))

        t1.start()
        t2.start()
        t3.start()

        t1.join()
        t2.join()
        t3.join()

        print("Waiting until all three resources get 'Running' state..")
        func_with_timeout(TestRaceConditions.all_resources_created, 350, sleep_time=3)

    def test_all_resources_created(self):
        # No duplicated organizations were created and automation config is consistent
        organizations = KubernetesTester.find_organizations(KubernetesTester.get_om_group_name())
        assert len(organizations) == 1
        groups = KubernetesTester.find_groups_in_organization(organizations[0], KubernetesTester.get_om_group_name())
        assert len(groups) == 1

        config = KubernetesTester.get_automation_config()

        # 2 replica sets for the sharded cluster and 1 - for the replica set
        assert len(config['replicaSets']) == 3

        # 1 standalone, 1 rs, 1 sharded cluster (each replica set contains single members)
        assert len(config['processes']) == 5

        assert len(config['sharding']) == 1

    def test_all_resources_removed(self):
        t1 = threading.Thread(target=KubernetesTester.delete_custom_resource,
                              args=(KubernetesTester.get_namespace(), "my-replica-set-single", "MongoDbReplicaSet"))
        t2 = threading.Thread(target=KubernetesTester.delete_custom_resource,
                              args=(KubernetesTester.get_namespace(), "my-standalone", "MongoDbStandalone"))
        t3 = threading.Thread(target=KubernetesTester.delete_custom_resource,
                              args=(KubernetesTester.get_namespace(), "sh001-single", "MongoDbShardedCluster"))

        t1.start()
        t2.start()
        t3.start()

        t1.join()
        t2.join()
        t3.join()
        func_with_timeout(TestRaceConditions.all_resources_removed, 200)

    @staticmethod
    def all_resources_created():
        rs_ready = KubernetesTester.check_phase(KubernetesTester.get_namespace(), "MongoDbReplicaSet",
                                                "my-replica-set-single", "Running")
        standalone_ready = KubernetesTester.check_phase(KubernetesTester.get_namespace(), "MongoDbStandalone",
                                                        "my-standalone", "Running")
        cluster_ready = KubernetesTester.check_phase(KubernetesTester.get_namespace(), "MongoDbShardedCluster",
                                                     "sh001-single", "Running")
        print("Standalone ready: {}, replica set ready: {}, sharded cluster ready: {}".format(
            standalone_ready, rs_ready, cluster_ready))
        return rs_ready and standalone_ready and cluster_ready

    @staticmethod
    def all_resources_removed():
        rs_ready = KubernetesTester.is_deleted(KubernetesTester.get_namespace(), "MongoDbReplicaSet",
                                               "my-replica-set-single")
        standalone_ready = KubernetesTester.is_deleted(KubernetesTester.get_namespace(), "MongoDbStandalone",
                                                       "my-standalone")
        cluster_ready = KubernetesTester.is_deleted(KubernetesTester.get_namespace(), "MongoDbShardedCluster",
                                                    "sh001-single")
        om_cleaned = func_with_assertions(KubernetesTester.check_om_state_cleaned)
        print("Standalone removed: {}, replica set removed: {}, sharded cluster removed: {}, Ops Manager cleaned: {}".
              format(standalone_ready, rs_ready, cluster_ready, om_cleaned))

        return rs_ready and standalone_ready and cluster_ready and om_cleaned
