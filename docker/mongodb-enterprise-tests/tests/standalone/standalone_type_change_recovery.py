import pytest
from kubetester import MongoDB
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from pytest import fixture


@fixture(scope="module")
def standalone(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("standalone.yaml"), "my-standalone", namespace
    )
    resource.set_version(custom_mdb_version)
    resource.create()

    return resource


@pytest.mark.e2e_standalone_type_change_recovery
def test_standalone_created(standalone: MongoDB):
    standalone.assert_reaches_phase(phase=Phase.Running)


@pytest.mark.e2e_standalone_type_change_recovery
def test_break_standalone(standalone: MongoDB):
    """Changes persistence configuration - this is not allowed by StatefulSet"""
    standalone.load()
    # Unfortunately even breaking the podtemplate won't get the resource into Failed state as it will just hang in Pending
    # standalone["spec"]["podSpec"] = {"podTemplate": {"spec": {"containers": [{"image": "broken", "name": "mongodb-enterprise-database"}]}}}
    standalone["spec"]["persistent"] = True
    standalone.update()
    standalone.assert_reaches_phase(
        phase=Phase.Failed,
        msg_regexp=".*can't execute update on forbidden fields.*",
        timeout=60,
    )


@pytest.mark.e2e_standalone_type_change_recovery
def test_fix_standalone(standalone: MongoDB):
    standalone.load()
    standalone["spec"]["persistent"] = False
    standalone.update()
    standalone.assert_reaches_phase(phase=Phase.Running)
