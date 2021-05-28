from pytest import fixture, mark

from kubetester.mongodb import Phase, MongoDB
from kubetester.kubetester import fixture as yaml_fixture


@fixture(scope="module")
def replicaset(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-basic.yaml"),
        namespace=namespace,
    )

    return resource.create()


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
    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    assert len(fc["policies"]) == 1
    assert fc["policies"][0]["policy"] == "EXTERNALLY_MANAGED_LOCK"
    assert fc["policies"][0]["disabledParams"] == []


@mark.e2e_feature_controls_authentication
def test_authentication_disabled_is_owned_by_operator(replicaset: MongoDB):
    """
    Authentication has been added to the Spec, on "disabled" mode,
    this makes the Operator to *own* authentication and thus
    making Feature controls API to restrict any
    """
    replicaset["spec"]["security"] = {"authentication": {"enabled": False}}
    replicaset.update()

    replicaset.assert_abandons_phase(Phase.Running)
    replicaset.assert_reaches_phase(Phase.Running)

    fc = replicaset.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    assert len(fc["policies"]) == 2
    assert fc["policies"][0]["disabledParams"] == []
    assert fc["policies"][1]["disabledParams"] == []

    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_AUTHENTICATION_MECHANISMS" in policies


@mark.e2e_feature_controls_authentication
def test_authentication_enabled_is_owned_by_operator(replicaset: MongoDB):
    """
    Authentication has been enabled on the Operator. Authentication is still
    owned by the operator so feature controls should be kept the same.
    """
    replicaset["spec"]["security"] = {
        "authentication": {"enabled": True, "modes": ["SCRAM"]}
    }
    replicaset.update()

    replicaset.assert_abandons_phase(Phase.Running)
    replicaset.assert_reaches_phase(Phase.Running, timeout=400)

    fc = replicaset.get_om_tester().get_feature_controls()

    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    assert len(fc["policies"]) == 2
    assert fc["policies"][0]["disabledParams"] == []
    assert fc["policies"][1]["disabledParams"] == []

    policies = [p["policy"] for p in fc["policies"]]
    assert "EXTERNALLY_MANAGED_LOCK" in policies
    assert "DISABLE_AUTHENTICATION_MECHANISMS" in policies


@mark.e2e_feature_controls_authentication
def test_authentication_disabled_owned_by_opsmanager(replicaset: MongoDB):
    """
    Authentication has been disabled (removed) on the Operator. Authentication
    is now "owned" by Ops Manager.
    """
    replicaset["spec"]["security"] = None
    replicaset.update()

    replicaset.assert_abandons_phase(Phase.Running)
    replicaset.assert_reaches_phase(Phase.Running)

    fc = replicaset.get_om_tester().get_feature_controls()

    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    assert len(fc["policies"]) == 1
    assert fc["policies"][0]["policy"] == "EXTERNALLY_MANAGED_LOCK"
    assert fc["policies"][0]["disabledParams"] == []
