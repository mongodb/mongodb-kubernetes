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
    resource = ClusterMongoDBRole.from_yaml(
        find_fixture("cluster-mongodb-role.yaml"), namespace="", cluster_scoped=True
    )

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

    return resource.update()


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

    return resource.update()


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

    return resource.update()


@mark.e2e_mongodb_custom_roles
def test_create_mongodb_role(mongodb_role: ClusterMongoDBRole):
    mongodb_role.assert_reaches_phase(Phase.Ready)


@mark.e2e_mongodb_custom_roles
def test_create_resources(replica_set: MongoDB, sharded_cluster: MongoDB, mc_replica_set: MongoDBMulti):
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
def test_removing_role_from_replica_set(replica_set: MongoDB):
    rs_version = replica_set.get_automation_config_tester().automation_config["version"]

    replica_set["spec"]["security"]["roleRefs"] = None
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running)
    wait_until(lambda: replica_set.get_automation_config_tester().reached_version(rs_version + 1), timeout=120)
    replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=0)


@mark.e2e_mongodb_custom_roles
def test_attempt_delete_role(mongodb_role: ClusterMongoDBRole):
    mongodb_role.assert_reaches_phase(Phase.Ready)

    mongodb_role.delete()

    # Resource should still exist since Sharded cluster and MCRS are still referencing it
    mongodb_role.assert_reaches_phase(Phase.Pending, timeout=400)

    assert mongodb_role["metadata"]["finalizers"][0] == "mongodb.com/v1.roleRemovalFinalizer"
    assert mongodb_role["metadata"]["deletionTimestamp"] is not None


@mark.e2e_mongodb_custom_roles
def test_remove_role_from_sharded_cluster(sharded_cluster: MongoDB, mongodb_role: ClusterMongoDBRole):
    sc_version = sharded_cluster.get_automation_config_tester().automation_config["version"]

    sharded_cluster["spec"]["security"]["roleRefs"] = None
    sharded_cluster.update()

    sharded_cluster.assert_reaches_phase(Phase.Running)
    wait_until(lambda: sharded_cluster.get_automation_config_tester().reached_version(sc_version + 1), timeout=120)
    sharded_cluster.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=0)

    # Resource should still exist since MCRS is still referencing it
    assert try_load(mongodb_role) == True

    assert mongodb_role["metadata"]["finalizers"][0] == "mongodb.com/v1.roleRemovalFinalizer"
    assert mongodb_role["metadata"]["deletionTimestamp"] is not None


@mark.e2e_mongodb_custom_roles
def test_remove_role_from_mc_replica_set(mc_replica_set: MongoDBMulti, mongodb_role: ClusterMongoDBRole):
    mcrs_version = mc_replica_set.get_automation_config_tester().automation_config["version"]

    mc_replica_set["spec"]["security"]["roleRefs"] = None
    mc_replica_set.update()

    mc_replica_set.assert_reaches_phase(Phase.Running)
    wait_until(lambda: mc_replica_set.get_automation_config_tester().reached_version(mcrs_version + 1), timeout=120)
    mc_replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=0)

    # No resources are referencing this role, should be gone
    assert try_load(mongodb_role) == False
