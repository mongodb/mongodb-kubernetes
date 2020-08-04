import yaml
import pytest
from pytest import fixture
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubernetes.client.rest import ApiException


@fixture(scope="function")
def mdb(namespace: str) -> str:
    return MongoDB.from_yaml(
        yaml_fixture("role-validation-base.yaml"), namespace=namespace
    )


# Basic testing for invalid empty values
@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_empty_role_name(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "",
            "db": "admin",
            "privileges": [
                {
                    "actions": ["insert"],
                    "resource": {"collection": "foo", "db": "admin"},
                }
            ],
        }
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Cannot create a role with an empty name",
    ):
        mdb.create()


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_empty_db_name(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "role",
            "db": "",
            "privileges": [
                {
                    "actions": ["insert"],
                    "resource": {"collection": "foo", "db": "admin"},
                }
            ],
        }
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Cannot create a role with an empty db",
    ):
        mdb.create()


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_inherited_role_empty_name(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
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
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Cannot inherit from a role with an empty name",
    ):
        mdb.create()


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_inherited_role_empty_db(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
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
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Cannot inherit from a role with an empty db",
    ):
        mdb.create()


# Testing for invalid authentication Restrictions
@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_client_source(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
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
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - AuthenticationRestriction is invalid - clientSource 355.127.0.1 is neither a valid IP address nor a valid CIDR range",
    ):
        mdb.create()


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_server_address(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
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
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - AuthenticationRestriction is invalid - serverAddress 355.127.0.1 is neither a valid IP address nor a valid CIDR range",
    ):
        mdb.create()


# Testing for invalid privileges
@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_cluster_and_db_collection(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "role",
            "db": "admin",
            "privileges": [
                {
                    "actions": ["insert"],
                    "resource": {"collection": "foo", "db": "admin", "cluster": True},
                }
            ],
        }
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Privilege is invalid - Cluster: true is not compatible with setting db/collection",
    ):
        mdb.create()


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_cluster_not_true(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "role",
            "db": "admin",
            "privileges": [{"actions": ["insert"], "resource": {"cluster": False}}],
        }
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Privilege is invalid - The only valid value for privilege.cluster, if set, is true",
    ):
        mdb.create()


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_action(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "role",
            "db": "admin",
            "privileges": [
                {
                    "actions": ["insertFoo"],
                    "resource": {"collection": "foo", "db": "admin"},
                }
            ],
        }
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Privilege is invalid - Actions are not valid - insertFoo is not a valid db action",
    ):
        mdb.create()


# Testing for privileges invalid for mongodb version
@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_privilege_for_mongodb_less_than_four_two(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "role",
            "db": "admin",
            "privileges": [
                {"actions": ["dropConnections"], "resource": {"cluster": True}}
            ],
        }
    ]
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Privilege is invalid - Actions are not valid -  Some of the provided actions are not valid for MongoDB 4.0.12",
    ):
        mdb.create()


@pytest.mark.e2e_mongodb_roles_validation_webhook
def test_invalid_privilege_for_mongodb_less_than_three_six(mdb: str):
    mdb["spec"]["security"]["roles"] = [
        {
            "role": "role",
            "db": "admin",
            "privileges": [
                {"actions": ["listSessions"], "resource": {"cluster": True}}
            ],
        }
    ]
    mdb["spec"]["version"] = "3.5.0"
    with pytest.raises(
        client.rest.ApiException,
        match="Error validating role - Privilege is invalid - Actions are not valid -  Some of the provided actions are not valid for MongoDB 3.5.0",
    ):
        mdb.create()
