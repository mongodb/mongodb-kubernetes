"""
This is a test which makes sure that update and delete calls issued together don't mess up
project state. Internal locks are used to ensure Ops Manager requests for update/delete operations
don't intersect (in headless mode the same is proven for the automation config Secret). Note that
K8s objects are removed right after the delete call is made (no serialization happens) so the
update operation doesn't succeed as internal locks don't affect K8s CR and dependent resources removal.
"""

from time import sleep

from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str, ops_manager) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-single.yaml"), "my-replica-set", namespace)
    resource.set_version(custom_mdb_version)
    try_load(resource)
    resource.configure(om=ops_manager, project_name=resource.name)
    return resource


@mark.e2e_replica_set_update_delete_parallel
def test_reaches_running_phase(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)
    replica_set.get_automation_config_tester().assert_replica_sets_size(1)

    # Goal state: derived from the automation config version + agent pod annotations in headless
    # mode, and from /automationStatus in (Cloud) Ops Manager mode.
    om_tester = replica_set.get_om_tester()
    om_tester.wait_agents_ready(timeout=120)
    status = om_tester.api_get_automation_status()
    assert {process["name"] for process in status["processes"]} == {f"{replica_set.name}-0"}
    assert all(
        process["lastGoalVersionAchieved"] == status["goalVersion"] for process in status["processes"]
    ), f"agents have not reached the automation config goal version: {status}"


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
