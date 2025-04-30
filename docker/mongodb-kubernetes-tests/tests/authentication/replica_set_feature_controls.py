import kubernetes
from kubetester import try_load, wait_until
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark
from tests.conftest import OPERATOR_NAME


@fixture(scope="function")
def replicaset(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-basic.yaml"),
        namespace=namespace,
    )
    if try_load(resource):
        return resource

    resource.update()
    return resource


@mark.e2e_feature_controls_authentication
def test_replicaset_reaches_running_phase(replicaset: MongoDB):
    replicaset.assert_reaches_phase(Phase.Running, ignore_errors=True)


@mark.e2e_feature_controls_authentication
def test_authentication_is_owned_by_opsmanager(replicaset: MongoDB):
    """
    There is no authentication, so feature controls API should allow
    authentication changes from Ops Manager UI.
    """
    fc = replicaset.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == OPERATOR_NAME

    assert len(fc["policies"]) == 2
    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_SET_MONGOD_VERSION" in policies

    for p in fc["policies"]:
        if p["policy"] == "EXTERNALLY_MANAGED_LOCK":
            assert p["disabledParams"] == []


@mark.e2e_feature_controls_authentication
def test_authentication_disabled_is_owned_by_operator(replicaset: MongoDB):
    """
    Authentication has been added to the Spec, on "disabled" mode,
    this makes the Operator to *own* authentication and thus
    making Feature controls API to restrict any
    """
    replicaset["spec"]["security"] = {"authentication": {"enabled": False}}
    replicaset.update()

    replicaset.assert_reaches_phase(Phase.Running, timeout=600)

    fc = replicaset.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == OPERATOR_NAME

    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert len(fc["policies"]) == 3

    assert policies[0]["disabledParams"] == []
    assert policies[2]["disabledParams"] == []

    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_AUTHENTICATION_MECHANISMS" in policies
    assert "DISABLE_SET_MONGOD_VERSION" in policies


@mark.e2e_feature_controls_authentication
def test_authentication_enabled_is_owned_by_operator(replicaset: MongoDB):
    """
    Authentication has been enabled on the Operator. Authentication is still
    owned by the operator so feature controls should be kept the same.
    """
    replicaset["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
    replicaset.update()

    replicaset.assert_reaches_phase(Phase.Running, timeout=600)

    fc = replicaset.get_om_tester().get_feature_controls()

    assert fc["externalManagementSystem"]["name"] == OPERATOR_NAME

    assert len(fc["policies"]) == 3
    # sort the policies to have pre-determined order
    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert policies[0]["disabledParams"] == []
    assert policies[2]["disabledParams"] == []

    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_AUTHENTICATION_MECHANISMS" in policies
    assert "DISABLE_SET_MONGOD_VERSION" in policies


@mark.e2e_feature_controls_authentication
def test_authentication_disabled_owned_by_opsmanager(replicaset: MongoDB):
    """
    Authentication has been disabled (removed) on the Operator. Authentication
    is now "owned" by Ops Manager.
    """
    last_transition = replicaset.get_status_last_transition_time()
    replicaset["spec"]["security"] = None
    replicaset.update()

    replicaset.assert_state_transition_happens(last_transition)
    replicaset.assert_reaches_phase(Phase.Running, timeout=600)

    fc = replicaset.get_om_tester().get_feature_controls()

    assert fc["externalManagementSystem"]["name"] == OPERATOR_NAME

    # sort the policies to have pre-determined order
    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert len(fc["policies"]) == 2
    assert policies[0]["policy"] == "DISABLE_SET_MONGOD_VERSION"
    assert policies[1]["policy"] == "EXTERNALLY_MANAGED_LOCK"
    assert policies[1]["disabledParams"] == []


@mark.e2e_om_feature_controls_authentication
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
    assert fc["externalManagementSystem"]["name"] == OPERATOR_NAME
    assert len(fc["policies"]) == 0
