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


def get_expected_role(role_name: str) -> dict:
    return {
        "role": role_name,
        "db": "admin",
        "roles": [
            {
                "db": "admin",
                "role": "read"
            }
        ],
        "privileges": [
            {
                "resource": {
                    "db": "config",
                    "collection": ""
                },
                "actions": [
                    "find",
                    "update",
                    "insert",
                    "remove"
                ]
            },
            {
                "resource": {
                    "db": "users",
                    "collection": "usersCollection"
                },
                "actions": [
                    "update",
                    "insert",
                    "remove"
                ]
            },
            {
                "resource": {
                    "db": "",
                    "collection": ""
                },
                "actions": [
                    "find"
                ]
            }
        ],
        "authenticationRestrictions": [
            {
                "clientSource": ["127.0.0.0/8"],
                "serverAddress": ["10.0.0.0/8"]
            }
        ]
    }


@fixture(scope="function")
def project_name_prefix(namespace: str) -> str:
    return random_k8s_name(f"{namespace}-project-")


@fixture(scope="function")
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


@fixture(scope="function")
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


@fixture(scope="function")
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


@fixture(scope="function")
def mongodb_role_with_empty_strings() -> ClusterMongoDBRole:
    resource = ClusterMongoDBRole.from_yaml(find_fixture("cluster-mongodb-role-with-empty-strings.yaml"), cluster_scoped=True)

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def mongodb_role_without_empty_strings() -> ClusterMongoDBRole:
    resource = ClusterMongoDBRole.from_yaml(find_fixture("cluster-mongodb-role-without-empty-strings.yaml"), cluster_scoped=True)

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def replica_set(namespace: str,
                mongodb_role_with_empty_strings: ClusterMongoDBRole,
                mongodb_role_without_empty_strings: ClusterMongoDBRole,
                first_project: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("replica-set-scram.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource["spec"]["members"] = 1
    resource["spec"]["security"]["roleRefs"] = [
        {
            "name": mongodb_role_with_empty_strings.get_name(),
            "kind": ClusterMongoDBRoleKind,
        },
        {
            "name": mongodb_role_without_empty_strings.get_name(),
            "kind": ClusterMongoDBRoleKind,
        },
    ]
    resource["spec"]["opsManager"]["configMapRef"]["name"] = first_project

    return resource


@fixture(scope="function")
def sharded_cluster(namespace: str,
                    mongodb_role_with_empty_strings: ClusterMongoDBRole,
                    mongodb_role_without_empty_strings: ClusterMongoDBRole,
                    second_project: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("sharded-cluster-scram-sha-1.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource["spec"]["mongodsPerShardCount"] = 1
    resource["spec"]["mongosCount"] = 1
    resource["spec"]["configServerCount"] = 1

    resource["spec"]["security"]["roleRefs"] = [
        {
            "name": mongodb_role_with_empty_strings.get_name(),
            "kind": ClusterMongoDBRoleKind,
        },
        {
            "name": mongodb_role_without_empty_strings.get_name(),
            "kind": ClusterMongoDBRoleKind,
        },
    ]
    resource["spec"]["opsManager"]["configMapRef"]["name"] = second_project

    return resource



@mark.e2e_mongodb_custom_roles_sc
def test_create_resources(
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
):
    mongodb_role_with_empty_strings.update()
    mongodb_role_without_empty_strings.update()

    replica_set.update()
    sharded_cluster.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_mongodb_custom_roles_sc
def test_automation_config_has_roles(
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole
):
    assert_expected_roles(replica_set, sharded_cluster, mongodb_role_with_empty_strings, mongodb_role_without_empty_strings)


def assert_expected_roles(replica_set: MongoDB,
                          sharded_cluster: MongoDB,
                          mongodb_role_with_empty_strings: ClusterMongoDBRole,
                          mongodb_role_without_empty_strings: ClusterMongoDBRole,
                          ):
    rs_tester = replica_set.get_automation_config_tester()
    sc_tester = sharded_cluster.get_automation_config_tester()
    rs_tester.assert_has_expected_number_of_roles(expected_roles=2)
    sc_tester.assert_has_expected_number_of_roles(expected_roles=2)

    rs_tester.assert_expected_role(role_index=0, expected_value=get_expected_role(mongodb_role_with_empty_strings.name))
    # the second role created without specifying fields with "" should result in identical role to the one with explicitly specified db: "", collection: "".
    rs_tester.assert_expected_role(role_index=1, expected_value=get_expected_role(mongodb_role_without_empty_strings.name))
    sc_tester.assert_expected_role(role_index=0, expected_value=get_expected_role(mongodb_role_with_empty_strings.name))
    sc_tester.assert_expected_role(role_index=1, expected_value=get_expected_role(mongodb_role_without_empty_strings.name))


@mark.e2e_mongodb_custom_roles_sc
def test_changing_role(
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole
):
    rs_version = replica_set.get_automation_config_tester().automation_config["version"]
    sc_version = sharded_cluster.get_automation_config_tester().automation_config["version"]

    mongodb_role_with_empty_strings["spec"]["roles"][0]["role"] = "readWrite"
    mongodb_role_with_empty_strings.update()

    wait_until(lambda: replica_set.get_automation_config_tester().reached_version(rs_version + 1), timeout=120)
    wait_until(lambda: sharded_cluster.get_automation_config_tester().reached_version(sc_version + 1), timeout=120)


@mark.e2e_mongodb_custom_roles_sc
def test_deleting_role_does_not_remove_access(
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mc_replica_set: MongoDBMulti,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
):
    mongodb_role_with_empty_strings.delete()

    assert try_load(mongodb_role_with_empty_strings) == False

    replica_set.assert_reaches_phase(
        phase=Phase.Failed, msg_regexp=f"ClusterMongoDBRole '{mongodb_role_with_empty_strings.get_name()}' not found"
    )
    sharded_cluster.assert_reaches_phase(
        phase=Phase.Failed, msg_regexp=f"ClusterMongoDBRole '{mongodb_role_with_empty_strings.get_name()}' not found"
    )
    mc_replica_set.assert_reaches_phase(
        phase=Phase.Failed, msg_regexp=f"ClusterMongoDBRole '{mongodb_role_with_empty_strings.get_name()}' not found"
    )

    # The role should still exist in the automation config
    replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=2)
    sharded_cluster.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=2)
    mc_replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=2)


@mark.e2e_mongodb_custom_roles_sc
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


@mark.e2e_mongodb_custom_roles_sc
def test_install_operator_with_clustermongodbroles_disabled(multi_cluster_operator_no_cluster_mongodb_roles):
    multi_cluster_operator_no_cluster_mongodb_roles.assert_is_running()


@mark.e2e_mongodb_custom_roles_sc
def test_replicaset_is_failed(replica_set: MongoDB):
    replica_set.assert_reaches_phase(
        Phase.Failed,
        msg_regexp="RoleRefs are not supported when ClusterMongoDBRoles are disabled. Please enable ClusterMongoDBRoles in the operator configuration.",
    )


@mark.e2e_mongodb_custom_roles_sc
def test_replicaset_is_reconciled_without_rolerefs(replica_set: MongoDB):
    rs_version = replica_set.get_automation_config_tester().automation_config["version"]
    replica_set["spec"]["security"]["roleRefs"] = None
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running)
    wait_until(lambda: replica_set.get_automation_config_tester().reached_version(rs_version + 1), timeout=120)

    replica_set.get_automation_config_tester().assert_has_expected_number_of_roles(expected_roles=0)
