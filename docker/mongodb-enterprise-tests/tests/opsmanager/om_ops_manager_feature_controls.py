from typing import Optional

import kubernetes
from kubetester import try_load, wait_until
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="mdb",
    ).configure(ops_manager, "mdb")
    resource.set_version(custom_mdb_version)

    try_load(resource)
    return resource


@mark.e2e_om_feature_controls
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_feature_controls
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@mark.e2e_om_feature_controls
def test_authentication_is_owned_by_opsmanager(replica_set: MongoDB):
    """
    There is no authentication, so feature controls API should allow
    authentication changes from Ops Manager UI.
    """
    fc = replica_set.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    assert len(fc["policies"]) == 2
    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_SET_MONGOD_VERSION" in policies

    for p in fc["policies"]:
        if p["policy"] == "EXTERNALLY_MANAGED_LOCK":
            assert p["disabledParams"] == []


@mark.e2e_om_feature_controls
def test_authentication_disabled_is_owned_by_operator(replica_set: MongoDB):
    """
    Authentication has been added to the Spec, on "disabled" mode,
    this makes the Operator to *own* authentication and thus
    making Feature controls API to restrict any
    """
    replica_set["spec"]["security"] = {"authentication": {"enabled": False}}
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    fc = replica_set.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert len(fc["policies"]) == 3

    assert policies[0]["disabledParams"] == []
    assert policies[2]["disabledParams"] == []

    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_AUTHENTICATION_MECHANISMS" in policies
    assert "DISABLE_SET_MONGOD_VERSION" in policies


@mark.e2e_om_feature_controls
def test_authentication_enabled_is_owned_by_operator(replica_set: MongoDB):
    """
    Authentication has been enabled on the Operator. Authentication is still
    owned by the operator so feature controls should be kept the same.
    """
    replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    fc = replica_set.get_om_tester().get_feature_controls()

    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    assert len(fc["policies"]) == 3
    # sort the policies to have pre-determined order
    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert policies[0]["disabledParams"] == []
    assert policies[2]["disabledParams"] == []

    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_AUTHENTICATION_MECHANISMS" in policies
    assert "DISABLE_SET_MONGOD_VERSION" in policies


@mark.e2e_om_feature_controls
def test_authentication_disabled_owned_by_opsmanager(replica_set: MongoDB):
    """
    Authentication has been disabled (removed) on the Operator. Authentication
    is now "owned" by Ops Manager.
    """
    last_transition = replica_set.get_status_last_transition_time()
    replica_set["spec"]["security"] = None
    replica_set.update()

    replica_set.assert_state_transition_happens(last_transition)
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    fc = replica_set.get_om_tester().get_feature_controls()

    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    # sort the policies to have pre-determined order
    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert len(fc["policies"]) == 2
    assert policies[0]["policy"] == "DISABLE_SET_MONGOD_VERSION"
    assert policies[1]["policy"] == "EXTERNALLY_MANAGED_LOCK"
    assert policies[1]["disabledParams"] == []


@mark.e2e_om_feature_controls
def test_feature_controls_cleared_on_replica_set_deletion(replica_set: MongoDB):
    """
    Replica set was deleted from the cluster. Policies are removed from the OpsManager group.
    """
    replica_set.delete()

    def replica_set_deleted() -> bool:
        k8s_resource_deleted = None
        try:
            replica_set.load()
            k8s_resource_deleted = False
        except kubernetes.client.ApiException:
            k8s_resource_deleted = True
        automation_config_deleted = None
        tester = replica_set.get_automation_config_tester()
        try:
            tester.assert_empty()
            automation_config_deleted = True
        except AssertionError:
            automation_config_deleted = False
        return k8s_resource_deleted and automation_config_deleted

    wait_until(replica_set_deleted, timeout=60)

    fc = replica_set.get_om_tester().get_feature_controls()

    # after deleting the replicaset the policies in the feature control are removed
    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"
    assert len(fc["policies"]) == 0
