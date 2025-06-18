from kubetester import (
    create_or_update_configmap,
    find_fixture,
    random_k8s_name,
    read_configmap,
    try_load,
    wait_until,
)
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_role import ClusterMongoDBRole, ClusterMongoDBRoleKind
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="module")
def project_name_prefix(namespace: str) -> str:
    return random_k8s_name(f"{namespace}-project-")


@fixture(scope="module")
def first_project(namespace: str, project_name_prefix: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{project_name_prefix}-first"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@fixture(scope="module")
def second_project(namespace: str, project_name_prefix: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{project_name_prefix}-second"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@fixture(scope="module")
def third_project(namespace: str, project_name_prefix: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{project_name_prefix}-third"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@fixture(scope="module")
def mongodb_role():
    resource = ClusterMongoDBRole.from_yaml(find_fixture("cluster-mongodb-role.yaml"), cluster_scoped=True)

    if try_load(resource):
        return resource

    return resource.update()


@fixture(scope="module")
def replica_set(namespace: str, mongodb_role: ClusterMongoDBRole, first_project: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("replica-set-scram.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource["spec"]["members"] = 1
    resource["spec"]["security"]["roleRefs"] = [
        {
            "name": mongodb_role.get_name(),
            "kind": ClusterMongoDBRoleKind,
        }
    ]
    resource["spec"]["opsManager"]["configMapRef"]["name"] = first_project

    return resource


@fixture(scope="module")
def sharded_cluster(namespace: str, mongodb_role: ClusterMongoDBRole, second_project: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("sharded-cluster-scram-sha-1.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource["spec"]["mongodsPerShardCount"] = 1
    resource["spec"]["mongosCount"] = 1
    resource["spec"]["configServerCount"] = 1

    resource["spec"]["security"]["roleRefs"] = [
        {
            "name": mongodb_role.get_name(),
            "kind": ClusterMongoDBRoleKind,
        }
    ]
    resource["spec"]["opsManager"]["configMapRef"]["name"] = second_project

    return resource


@fixture(scope="module")
def mc_replica_set(namespace: str, mongodb_role: ClusterMongoDBRole, third_project: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(find_fixture("mongodb-multi.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource["spec"]["security"] = {
        "roleRefs": [
            {
                "name": mongodb_role.get_name(),
                "kind": ClusterMongoDBRoleKind,
            }
        ]
    }
    resource["spec"]["opsManager"]["configMapRef"]["name"] = third_project
    resource["spec"]["clusterSpecList"] = cluster_spec_list(["kind-e2e-cluster-1"], [1])

    return resource


@mark.e2e_mongodb_custom_roles
def test_create_resources(
    mongodb_role: ClusterMongoDBRole, replica_set: MongoDB, sharded_cluster: MongoDB, mc_replica_set: MongoDBMulti
):
    replica_set.update()
    sharded_cluster.update()
    mc_replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)
    mc_replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_mongodb_custom_roles
def test_automation_config_has_roles(
    replica_set: MongoDB, sharded_cluster: MongoDB, mc_replica_set: MongoDBMulti, mongodb_role: ClusterMongoDBRole
):
    rs_tester = replica_set.get_automation_config_tester()
    rs_tester.assert_has_expected_number_of_roles(expected_roles=1)
    rs_tester.assert_expected_role(role_index=0, expected_value=mongodb_role.get_role())

    sc_tester = sharded_cluster.get_automation_config_tester()
    sc_tester.assert_has_expected_number_of_roles(expected_roles=1)
    sc_tester.assert_expected_role(role_index=0, expected_value=mongodb_role.get_role())

    mcrs_tester = mc_replica_set.get_automation_config_tester()
    mcrs_tester.assert_has_expected_number_of_roles(expected_roles=1)
    mcrs_tester.assert_expected_role(role_index=0, expected_value=mongodb_role.get_role())


@mark.e2e_mongodb_custom_roles
def test_changing_role(
    replica_set: MongoDB, sharded_cluster: MongoDB, mc_replica_set: MongoDBMulti, mongodb_role: ClusterMongoDBRole
):
    rs_version = replica_set.get_automation_config_tester().automation_config["version"]
    sc_version = sharded_cluster.get_automation_config_tester().automation_config["version"]
    mcrs_version = mc_replica_set.get_automation_config_tester().automation_config["version"]

    mongodb_role["spec"]["roles"][0]["role"] = "readWrite"
    mongodb_role.update()

    wait_until(lambda: replica_set.get_automation_config_tester().reached_version(rs_version + 1), timeout=120)
    wait_until(lambda: sharded_cluster.get_automation_config_tester().reached_version(sc_version + 1), timeout=120)
    wait_until(lambda: mc_replica_set.get_automation_config_tester().reached_version(mcrs_version + 1), timeout=120)

    replica_set.get_automation_config_tester().assert_expected_role(
        role_index=0, expected_value=mongodb_role.get_role()
    )
    sharded_cluster.get_automation_config_tester().assert_expected_role(
        role_index=0, expected_value=mongodb_role.get_role()
    )
    mc_replica_set.get_automation_config_tester().assert_expected_role(
        role_index=0, expected_value=mongodb_role.get_role()
    )


@mark.e2e_mongodb_custom_roles
def test_deleting_role_does_not_remove_access(
    mongodb_role: ClusterMongoDBRole, replica_set: MongoDB, sharded_cluster: MongoDB, mc_replica_set: MongoDBMulti
):
    mongodb_role.delete()

    assert try_load(mongodb_role) == False

    replica_set.assert_reaches_phase(
        phase=Phase.Failed, msg_regexp=f"ClusterMongoDBRole '{mongodb_role.get_name()}' not found"
    )
    sharded_cluster.assert_reaches_phase(
        phase=Phase.Failed, msg_regexp=f"ClusterMongoDBRole '{mongodb_role.get_name()}' not found"
    )
    mc_replica_set.assert_reaches_phase(
        phase=Phase.Failed, msg_regexp=f"ClusterMongoDBRole '{mongodb_role.get_name()}' not found"
    )

    # The role should still exist in the automation config
    replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=1)
    sharded_cluster.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=1)
    mc_replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=1)


@mark.e2e_mongodb_custom_roles
def test_removing_role_from_resources(replica_set: MongoDB, sharded_cluster: MongoDB, mc_replica_set: MongoDBMulti):
    sc_version = sharded_cluster.get_automation_config_tester().automation_config["version"]
    mcrs_version = mc_replica_set.get_automation_config_tester().automation_config["version"]

    sharded_cluster["spec"]["security"]["roleRefs"] = None
    sharded_cluster.update()

    mc_replica_set["spec"]["security"]["roleRefs"] = None
    mc_replica_set.update()

    wait_until(lambda: sharded_cluster.get_automation_config_tester().reached_version(sc_version + 1), timeout=120)
    wait_until(lambda: mc_replica_set.get_automation_config_tester().reached_version(mcrs_version + 1), timeout=120)

    sharded_cluster.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=0)
    mc_replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=0)


@mark.e2e_mongodb_custom_roles
def test_install_operator_with_clustermongodbroles_disabled(multi_cluster_operator_no_cluster_mongodb_roles):
    multi_cluster_operator_no_cluster_mongodb_roles.assert_is_running()


@mark.e2e_mongodb_custom_roles
def test_replicaset_is_failed(replica_set: MongoDB):
    replica_set.assert_reaches_phase(
        Phase.Failed,
        msg_regexp="RoleRefs are not supported when ClusterMongoDBRoles are disabled. Please enable ClusterMongoDBRoles in the operator configuration.",
    )


@mark.e2e_mongodb_custom_roles
def test_replicaset_is_reconciled_without_rolerefs(replica_set: MongoDB):
    rs_version = replica_set.get_automation_config_tester().automation_config["version"]
    replica_set["spec"]["security"]["roleRefs"] = None
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running)
    wait_until(lambda: replica_set.get_automation_config_tester().reached_version(rs_version + 1), timeout=120)

    replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=0)
