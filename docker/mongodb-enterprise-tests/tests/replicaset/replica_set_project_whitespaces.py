from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-single.yaml"), namespace=namespace,
    )
    return resource


@fixture(scope="module")
def project_name_with_whitespace(replica_set: MongoDB) -> str:
    """ replaces the value in the ConfigMap with the generated one with whitespaces in it """
    project_name = KubernetesTester.random_om_project_name()
    connection_data = replica_set.read_configmap()
    connection_data["projectName"] = project_name
    KubernetesTester.update_configmap(
        replica_set.namespace, replica_set.config_map_name, connection_data
    )
    yield project_name

    print()
    print(
        f"Removing the generated group '{project_name}' from Ops Manager/Cloud Manager"
    )
    print(replica_set.get_om_tester().api_remove_group())


@mark.e2e_replica_set_project_whitespaces
@mark.usefixtures("project_name_with_whitespace")
def test_replica_set_created(replica_set: MongoDB):
    replica_set.create()
    replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_replica_set_project_whitespaces
def test_project_name(replica_set: MongoDB, project_name_with_whitespace: str):
    om_tester = replica_set.get_om_tester()
    assert om_tester.context.group_name == project_name_with_whitespace
    om_tester.api_get_group_in_organization(
        om_tester.context.org_id, om_tester.context.group_name
    )
