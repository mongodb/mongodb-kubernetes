"""
Ensures that validation warnings for ops manager reflect its current state
"""

from typing import Optional

import pytest
from kubernetes.client.rest import ApiException
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

APPDB_SHARD_COUNT_WARNING = "ShardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"


@mark.e2e_om_validation_webhook
def test_wait_for_webhook(namespace: str, default_operator: Operator):
    default_operator.wait_for_webhook()


def om_validation(namespace: str) -> MongoDBOpsManager:
    return MongoDBOpsManager.from_yaml(yaml_fixture("om_validation.yaml"), namespace=namespace)


@mark.e2e_om_validation_webhook
def test_connectivity_not_allowed_in_appdb(namespace: str):
    om = om_validation(namespace)

    om["spec"]["applicationDatabase"]["connectivity"] = {"replicaSetHorizons": [{"test-horizon": "dfdfdf"}]}

    with pytest.raises(
        ApiException,
        match=r"connectivity field is not configurable for application databases",
    ):
        om.create()


@mark.e2e_om_validation_webhook
def test_opsmanager_version(namespace: str):
    om = om_validation(namespace)
    om["spec"]["version"] = "4.4.4.4"

    with pytest.raises(ApiException, match=r"is an invalid value for spec.version"):
        om.create()


@mark.e2e_om_validation_webhook
def test_appdb_version(namespace: str):
    om = om_validation(namespace)
    om["spec"]["applicationDatabase"]["version"] = "4.4.10.10"

    # this exception is raised by CRD regexp validation for the version, not our internal one
    with pytest.raises(ApiException, match=r"spec.applicationDatabase.version in body should match"):
        om.create()

    om["spec"]["applicationDatabase"]["version"] = "3.6.12"
    with pytest.raises(ApiException, match=r"the version of Application Database must be \\u003e= 4.0"):
        om.create()


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    return om.create()


@mark.e2e_om_validation_webhook
class TestOpsManagerValidationWarnings:
    def test_disable_webhook(self, default_operator: Operator):
        default_operator.disable_webhook()
