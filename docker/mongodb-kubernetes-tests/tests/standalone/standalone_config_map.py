import pytest
from kubernetes import client
from kubernetes.client import V1ConfigMap
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture


@fixture(scope="module")
def standalone(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("standalone.yaml"), namespace=namespace)
    return resource.create()


@fixture(scope="module")
def new_project_name(standalone: MongoDB) -> str:
    yield KubernetesTester.random_om_project_name()
    # Cleaning the new group in any case - the updated config map will be used to get the new name
    print("\nRemoving the generated group from Ops Manager/Cloud Manager")
    print(standalone.get_om_tester().api_remove_group())


@pytest.mark.e2e_standalone_config_map
class TestStandaloneListensConfigMap:
    group_id = ""

    def test_create_standalone(self, standalone: MongoDB):
        standalone.assert_reaches_phase(Phase.Running, timeout=150)

    def test_patch_config_map(self, standalone: MongoDB, new_project_name: str):
        # saving the group id for later check
        TestStandaloneListensConfigMap.group_id = standalone.get_om_tester().find_group_id()

        config_map = V1ConfigMap(data={"projectName": new_project_name})
        client.CoreV1Api().patch_namespaced_config_map(standalone.config_map_name, standalone.namespace, config_map)

        print('\nPatched the ConfigMap - changed group name to "{}"'.format(new_project_name))

    def test_standalone_handles_changes(self, standalone: MongoDB):
        standalone.assert_abandons_phase(phase=Phase.Running)
        standalone.assert_reaches_phase(Phase.Running, timeout=200)

    def test_new_group_was_created(self, standalone: MongoDB):
        # Checking that the new group was created in OM
        assert standalone.get_om_tester().find_group_id() != TestStandaloneListensConfigMap.group_id
