from kubetester import (
    create_or_update_configmap,
    find_fixture,
    read_configmap,
    try_load,
    wait_until,
)
from kubetester.mongodb import MongoDB
from kubetester.mongodb_role import ClusterMongoDBRole, ClusterMongoDBRoleKind
from pytest import fixture, mark
from tests.authentication.shared import custom_roles as testhelper
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="function")
def first_project(namespace: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{namespace}-first"
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
def second_project(namespace: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{namespace}-second"
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
def third_project(namespace: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{namespace}-third"
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
    resource = ClusterMongoDBRole.from_yaml(
        find_fixture("cluster-mongodb-role-with-empty-strings.yaml"), cluster_scoped=True
    )

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def mongodb_role_without_empty_strings() -> ClusterMongoDBRole:
    resource = ClusterMongoDBRole.from_yaml(
        find_fixture("cluster-mongodb-role-without-empty-strings.yaml"), cluster_scoped=True
    )

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def replica_set(
    namespace: str,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
    first_project: str,
) -> MongoDB:
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
def sharded_cluster(
    namespace: str,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
    second_project: str,
) -> MongoDB:
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


@fixture(scope="function")
def mc_replica_set(
    namespace: str,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
    third_project: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("mongodb-multi.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource["spec"]["security"] = {
        "roleRefs": [
            {
                "name": mongodb_role_with_empty_strings.get_name(),
                "kind": ClusterMongoDBRoleKind,
            },
            {
                "name": mongodb_role_without_empty_strings.get_name(),
                "kind": ClusterMongoDBRoleKind,
            },
        ]
    }
    resource["spec"]["opsManager"]["configMapRef"]["name"] = third_project
    resource["spec"]["clusterSpecList"] = cluster_spec_list(["kind-e2e-cluster-1"], [1])

    return resource


@mark.e2e_mongodb_custom_roles
def test_create_resources(
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mc_replica_set: MongoDB,
):
    testhelper.test_create_resources(
        mongodb_role_with_empty_strings,
        mongodb_role_without_empty_strings,
        replica_set,
        sharded_cluster,
        mc_replica_set,
    )


@mark.e2e_mongodb_custom_roles
def test_automation_config_has_roles(
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mc_replica_set: MongoDB,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
):
    testhelper.test_automation_config_has_roles(
        replica_set,
        sharded_cluster,
        mc_replica_set,
        mongodb_role_with_empty_strings,
        mongodb_role_without_empty_strings,
    )


def assert_expected_roles(
    mc_replica_set: MongoDB,
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
):
    testhelper.assert_expected_roles(
        mc_replica_set,
        replica_set,
        sharded_cluster,
        mongodb_role_with_empty_strings,
        mongodb_role_without_empty_strings,
    )


@mark.e2e_mongodb_custom_roles
def test_change_inherited_role(
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mc_replica_set: MongoDB,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
    mongodb_role_without_empty_strings: ClusterMongoDBRole,
):
    testhelper.test_change_inherited_role(
        replica_set,
        sharded_cluster,
        mc_replica_set,
        mongodb_role_with_empty_strings,
        mongodb_role_without_empty_strings,
    )


@mark.e2e_mongodb_custom_roles
def test_deleting_role_does_not_remove_access(
    replica_set: MongoDB,
    sharded_cluster: MongoDB,
    mc_replica_set: MongoDB,
    mongodb_role_with_empty_strings: ClusterMongoDBRole,
):
    testhelper.test_deleting_role_does_not_remove_access(
        replica_set, sharded_cluster, mc_replica_set, mongodb_role_with_empty_strings
    )


@mark.e2e_mongodb_custom_roles
def test_removing_role_from_resources(replica_set: MongoDB, sharded_cluster: MongoDB, mc_replica_set: MongoDB):
    testhelper.test_removing_role_from_resources(replica_set, sharded_cluster, mc_replica_set)


@mark.e2e_mongodb_custom_roles
def test_install_operator_with_clustermongodbroles_disabled(multi_cluster_operator_no_cluster_mongodb_roles):
    testhelper.test_install_operator_with_clustermongodbroles_disabled(multi_cluster_operator_no_cluster_mongodb_roles)


@mark.e2e_mongodb_custom_roles
def test_replicaset_is_failed(replica_set: MongoDB):
    testhelper.test_replicaset_is_failed(replica_set)


@mark.e2e_mongodb_custom_roles
def test_replicaset_is_reconciled_without_rolerefs(replica_set: MongoDB):
    testhelper.test_replicaset_is_reconciled_without_rolerefs(replica_set)
