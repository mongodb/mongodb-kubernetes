import yaml
from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
    KubernetesTester,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

BUNDLED_APP_DB_VERSION = "4.2.2-ent"


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    KubernetesTester.make_default_gp2_storage_class()

    with open(yaml_fixture("mongodb_versions_claim.yaml"), "r") as f:
        pvc_body = yaml.safe_load(f.read())
    KubernetesTester.create_pvc(namespace, body=pvc_body)

    with open(yaml_fixture("download_mongodb_versions.yaml"), "r") as f:
        pod_body = yaml.safe_load(f.read())
    pod_body["metadata"]["name"] = pod_body["metadata"]["name"] + "-" + namespace
    KubernetesTester.create_pod(namespace, body=pod_body)

    def pod_is_completed() -> bool:
        try:
            pod = KubernetesTester.read_pod(namespace, pod_body["metadata"]["name"])
            conditions = pod.status.conditions
            completed_pods = [
                cond
                for cond in conditions
                if (cond.reason == "PodCompleted" and cond.status == "True")
            ]
            return len(completed_pods) == 1
        except Exception:
            return False

    # we need to wait for the pod to be completed before we continue with using the persistent volume
    # with Ops Manager. "pod has unbound immediate PersistentVolumeClaims"
    # Once this Pod is completed, the required mongodb versions are copied into the pv.
    KubernetesTester.wait_until(pod_is_completed, timeout=300)

    # remove the pod as soon as it has completed, as we don't need it for anything else
    KubernetesTester.delete_pod(namespace, pod_body["metadata"]["name"])

    """ The fixture for Ops Manager to be created."""
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_localmode-single-pv.yaml"), namespace=namespace
    )
    yield om.create()

    KubernetesTester.delete_pvc(namespace, "mongodb-versions-claim")


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"), namespace=namespace,
    ).configure(ops_manager, "my-replica-set")
    resource["spec"]["version"] = "4.0.0"  # invalid version
    yield resource.create()


@mark.e2e_om_localmode
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)
    assert ops_manager.appdb_status().get_members() == 3
    assert ops_manager.appdb_status().get_version() == BUNDLED_APP_DB_VERSION


@mark.e2e_om_localmode
def test_admin_config_map(ops_manager: MongoDBOpsManager):
    ops_manager.get_automation_config_tester().reached_version(1)


@skip_if_local
@mark.e2e_om_localmode
def test_mongod(ops_manager: MongoDBOpsManager):
    mdb_tester = ops_manager.get_appdb_tester()
    mdb_tester.assert_connectivity()
    mdb_tester.assert_version("4.2.2")


@mark.e2e_om_localmode
def test_appdb_automation_config(ops_manager: MongoDBOpsManager):
    expected_roles = {
        ("admin", "readWriteAnyDatabase"),
        ("admin", "dbAdminAnyDatabase"),
        ("admin", "clusterMonitor"),
    }

    # only user should be the Ops Manager user
    tester = ops_manager.get_automation_config_tester(
        expected_users=1, authoritative_set=False,
    )
    tester.assert_authentication_mechanism_enabled("MONGODB-CR")
    tester.assert_has_user("mongodb-ops-manager")
    tester.assert_user_has_roles("mongodb-ops-manager", expected_roles)


@mark.e2e_om_localmode
def test_ops_manager_reaches_running_phase(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_localmode
def test_replica_set_reaches_failed_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase(
        Phase.Failed,
        msg_regexp=".*Invalid config: MongoDB version 4.0.0 is not available.*",
    )


@mark.e2e_om_localmode
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set["spec"]["version"] = "4.0.2"  # the version in the pv
    replica_set.update()
    replica_set.assert_abandons_phase(Phase.Failed)
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@skip_if_local
@mark.e2e_om_localmode
def test_client_can_connect_to_mongodb(replica_set: MongoDB):
    replica_set.assert_connectivity()


@mark.e2e_om_localmode
def test_restart_ops_manager_pod(ops_manager: MongoDBOpsManager):
    ops_manager["spec"]["configuration"]["mms.testUtil.enabled"] = "false"
    ops_manager.update()
    ops_manager.om_status().assert_abandons_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_localmode
def test_can_scale_replica_set(replica_set: MongoDB):
    replica_set["spec"]["members"] = 5
    replica_set.update()
    replica_set.assert_abandons_phase(Phase.Running)
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_local
@mark.e2e_om_localmode
def test_client_can_still_connect(replica_set: MongoDB):
    replica_set.assert_connectivity()
