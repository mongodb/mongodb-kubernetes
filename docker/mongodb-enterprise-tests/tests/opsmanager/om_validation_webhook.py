"""
Ensures that validation warnings for ops manager reflect its current state
"""
import time
from typing import Optional

from kubernetes import client
from kubernetes.client.rest import ApiException

from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.operator import Operator

import pytest
from pytest import fixture, mark

APPDB_SHARD_COUNT_WARNING = "ShardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"


@mark.e2e_om_validation_webhook
def test_wait_for_webhook(namespace: str, default_operator: Operator):
    default_operator.wait_for_webhook()


def om_validation(namespace: str) -> MongoDBOpsManager:
    return MongoDBOpsManager.from_yaml(
        yaml_fixture("om_validation.yaml"), namespace=namespace
    )


@mark.e2e_om_validation_webhook
def test_appdb_shardcount_invalid(namespace: str):
    om = om_validation(namespace)

    om["spec"]["applicationDatabase"]["shardCount"] = 2

    with pytest.raises(
        ApiException,
        match=r"shardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets",
    ):
        om.create()


@mark.e2e_om_validation_webhook
def test_podspec_not_configurable_for_opsmanager(namespace: str):
    om = om_validation(namespace)

    om["spec"]["podSpec"] = {}

    with pytest.raises(
        ApiException,
        match=r"podSpec field is not configurable for Ops Manager, use the statefulSet field instead",
    ):
        om.create()


@mark.e2e_om_validation_webhook
def test_podspec_not_configurable_for_opsmanager_backup(namespace: str):
    om = om_validation(namespace)
    om["spec"]["backup"]["podSpec"] = {}

    with pytest.raises(
        ApiException,
        match=r"podSpec field is not configurable for Ops Manager Backup, use the backup.statefulSet field instead",
    ):
        om.create()


@mark.e2e_om_validation_webhook
def test_connectivity_not_allowed_in_appdb(namespace: str):
    om = om_validation(namespace)

    om["spec"]["applicationDatabase"]["connectivity"] = {
        "replicaSetHorizons": [{"test-horizon": "dfdfdf"}]
    }

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


@fixture(scope="module")
def ops_manager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om["spec"]["applicationDatabase"]["shardCount"] = 3
    return om.create()


@mark.e2e_om_validation_webhook
class TestOpsManagerValidationWarnings:
    def test_disable_webhook(self, default_operator: Operator):
        default_operator.disable_webhook()

    def test_create_om_failed_with_message(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Failed, timeout=300)

        assert APPDB_SHARD_COUNT_WARNING == ops_manager.om_status().get_message()

        # Warnings are not created here!
        assert "warnings" not in ops_manager.get_status()

    def test_update_om_with_corrections(self, ops_manager: MongoDBOpsManager):
        del ops_manager["spec"]["applicationDatabase"]["shardCount"]
        # TODO add replace() method to kubeobject
        client.CustomObjectsApi().replace_namespaced_custom_object(
            ops_manager.group,
            ops_manager.version,
            ops_manager.namespace,
            ops_manager.plural,
            ops_manager.name,
            ops_manager.backing_obj,
        )

        def has_no_warnings(om: MongoDBOpsManager) -> bool:
            return "warnings" not in om.get_status()

        ops_manager.assert_reaches(has_no_warnings, timeout=1200)
        ops_manager.om_status().assert_reaches_phase(Phase.Failed, timeout=300)

    def test_warnings_reset(self, ops_manager: MongoDBOpsManager):
        assert "warnings" not in ops_manager.get_status()
