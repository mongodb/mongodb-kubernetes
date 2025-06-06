import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_role import ClusterMongoDBRole
from kubetester.operator import Operator
from pytest import fixture


@fixture(scope="function")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("role-validation-base.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource


@fixture(scope="function")
def mdbr() -> ClusterMongoDBRole:
    resource = ClusterMongoDBRole.from_yaml(
        yaml_fixture("cluster_mongodb_role_base.yaml"), namespace="", cluster_scoped=True
    )
    return resource


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_wait_for_webhook(namespace: str, default_operator: Operator):
    default_operator.wait_for_webhook()


# Basic testing for invalid empty values
@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_empty_role_name(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "",
        "db": "admin",
        "privileges": [
            {
                "actions": ["insert"],
                "resource": {"collection": "foo", "db": "admin"},
            }
        ],
    }

    err_msg = "Cannot create a role with an empty name"

    _assert_role_error(mdb, mdbr, role, err_msg)


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_empty_db_name(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "",
        "privileges": [
            {
                "actions": ["insert"],
                "resource": {"collection": "foo", "db": "admin"},
            }
        ],
    }

    err_msg = "Cannot create a role with an empty db"

    _assert_role_error(mdb, mdbr, role, err_msg)


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_inherited_role_empty_name(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "admin",
        "privileges": [
            {
                "actions": ["insert"],
                "resource": {"collection": "foo", "db": "admin"},
            }
        ],
        "roles": [{"db": "admin", "role": ""}],
    }

    err_msg = "Cannot inherit from a role with an empty name"

    _assert_role_error(mdb, mdbr, role, err_msg)


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_inherited_role_empty_db(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "admin",
        "privileges": [
            {
                "actions": ["insert"],
                "resource": {"collection": "foo", "db": "admin"},
            }
        ],
        "roles": [{"db": "", "role": "role"}],
    }

    err_msg = "Cannot inherit from a role with an empty db"

    _assert_role_error(mdb, mdbr, role, err_msg)


# Testing for invalid authentication Restrictions
@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_client_source(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "admin",
        "privileges": [
            {
                "actions": ["insert"],
                "resource": {"collection": "foo", "db": "admin"},
            }
        ],
        "authenticationRestrictions": [{"clientSource": ["355.127.0.1"]}],
    }

    err_msg = "AuthenticationRestriction is invalid - clientSource 355.127.0.1 is neither a valid IP address nor a valid CIDR range"

    _assert_role_error(mdb, mdbr, role, err_msg)


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_server_address(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "admin",
        "privileges": [
            {
                "actions": ["insert"],
                "resource": {"collection": "foo", "db": "admin"},
            }
        ],
        "authenticationRestrictions": [{"serverAddress": ["355.127.0.1"]}],
    }

    err_msg = "AuthenticationRestriction is invalid - serverAddress 355.127.0.1 is neither a valid IP address nor a valid CIDR range"

    _assert_role_error(mdb, mdbr, role, err_msg)


# Testing for invalid privileges
@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_cluster_and_db_collection(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "admin",
        "privileges": [
            {
                "actions": ["insert"],
                "resource": {"collection": "foo", "db": "admin", "cluster": True},
            }
        ],
    }

    err_msg = "Privilege is invalid - Cluster: true is not compatible with setting db/collection"

    _assert_role_error(mdb, mdbr, role, err_msg)


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_cluster_not_true(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "admin",
        "privileges": [{"actions": ["insert"], "resource": {"cluster": False}}],
    }

    err_msg = "Privilege is invalid - The only valid value for privilege.cluster, if set, is true"

    _assert_role_error(mdb, mdbr, role, err_msg)


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_action(mdb: MongoDB, mdbr: ClusterMongoDBRole):
    role = {
        "role": "role",
        "db": "admin",
        "privileges": [
            {
                "actions": ["insertFoo"],
                "resource": {"collection": "foo", "db": "admin"},
            }
        ],
    }
    err_msg = "Privilege is invalid - Actions are not valid - insertFoo is not a valid db action"

    _assert_role_error(mdb, mdbr, role, err_msg)


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_roles_and_role_refs(mdb: MongoDB):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "role",
            "db": "admin",
            "roles": [
                {
                    "role": "root",
                    "db": "admin",
                }
            ],
        }
    ]
    mdb["spec"]["security"]["roleRefs"] = [
        {
            "name": "test-clusterrole",
            "kind": "ClusterMongoDBRole",
        }
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="At most one of roles or roleRefs can be non-empty",
    ):
        mdb.create()


def _assert_role_error(mdb: MongoDB, mdbr: ClusterMongoDBRole, role, err_msg):
    mdb["spec"]["security"]["roles"] = [role]

    with pytest.raises(
        client.rest.ApiException,
        match=f"Error validating role - {err_msg}",
    ):
        mdb.create()

    mdbr["spec"] = role
    mdbr.create()
    mdb["spec"]["security"]["roles"] = []
    mdb["spec"]["security"]["roleRefs"] = [
        {"name": mdbr.get_name(), "kind": mdbr.kind},
    ]

    mdb.create()
    mdb.assert_reaches_phase(phase=Phase.Failed, msg_regexp=f"Error validating role '{mdbr.get_name()}' - {err_msg}")

    mdb.delete()
    mdbr.delete()
