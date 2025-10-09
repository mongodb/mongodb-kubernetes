"""
This is a test which makes sure that update and delete calls issued together don't mess up
OM group state. Internal locks are used to ensure OM requests for update/delete operations
don't intersect. Note that K8s objects are removed right after the delete call is made(no
serialization happens) so update operation doesn't succeed as internal locks don't affect
K8s CR and dependent resources removal.
"""

from time import sleep

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-single.yaml"), "my-replica-set", namespace)
    resource.set_version(custom_mdb_version)
    resource.create()

    return resource


@mark.e2e_replica_set_update_delete_parallel
def test_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running)
    replica_set.get_automation_config_tester().assert_replica_sets_size(1)


@mark.e2e_replica_set_update_delete_parallel
def test_update_delete_in_parallel(replica_set: MongoDB):
    replica_set["spec"]["members"] = 2
    replica_set.update()
    sleep(5)
    replica_set.delete()

    om_tester = replica_set.get_om_tester()

    def om_is_clean():
        try:
            om_tester.assert_hosts_empty()
            return True
        except AssertionError:
            return False

    run_periodically(om_is_clean, timeout=180)
