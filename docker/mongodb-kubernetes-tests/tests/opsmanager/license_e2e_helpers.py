"""Shared helpers, fixtures, and constants for Ops Manager license E2E tests."""

import time
from typing import Optional

import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

logger = test_logger.get_test_logger(__name__)

OM_NAME = "om-license-e2e"
DEFAULT_TIMEOUT = 90


RECOMPUTE_ANNOTATION = "mongodb.com/license-recompute"

RELEASED_STATUS = "RELEASED"

LICENSE_READY_CONDITION = "LicenseReady"
LICENSE_MANAGED_CONDITION = "LicenseManaged"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_license.yaml"), namespace=namespace, name=OM_NAME
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["license"] = None
    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


def om_holder_id(ops_manager: MongoDBOpsManager) -> str:
    return ops_manager.get_om_tester().api_read_license_identity()["holderId"]


def om_condition(ops_manager: MongoDBOpsManager, condition_type: str) -> Optional[dict]:
    for condition in ops_manager["status"]["opsManager"]["conditions"]:
        if condition["type"] == condition_type:
            return condition
    return None


def generation_of(ops_manager: MongoDBOpsManager) -> int:
    ops_manager.load()
    return ops_manager.get_generation()


def license_ready_condition(ops_manager: MongoDBOpsManager) -> dict:
    condition = om_condition(ops_manager, LICENSE_READY_CONDITION)
    assert condition is not None, "the CR has no LicenseReady condition"
    return condition


def _managed_as(ops_manager: MongoDBOpsManager, generation: int, installed: Optional[str]) -> bool:
    """Whether LicenseManaged settled this generation as expected. An empty installed expects no
    license from secretRef, an artifact id expects Ops Manager to hold it, and None skips the check."""
    condition = _managed_condition(ops_manager, generation)
    if condition is None:
        return False
    if installed is None:
        return True
    if installed == "":
        return condition["reason"] == "LicenseNotProvided"
    return condition["reason"] == "LicenseManaged" and installed in condition["message"]


def _managed_condition(ops_manager: MongoDBOpsManager, generation: int) -> Optional[dict]:
    """The LicenseManaged condition, or None while it still describes an older generation.

    Fails at once when the operator settled this generation as not managed, a failed reconcile or a
    rejected license.
    """
    condition = om_condition(ops_manager, LICENSE_MANAGED_CONDITION)
    if condition is None or condition["observedGeneration"] < generation:
        return None
    if condition["reason"] in ("LicenseNotManaged", "LicenseReconcileFailed"):
        pytest.fail(f"LicenseManaged is {condition['reason']}: {condition['message']}")
    return condition


def read_om_license_view(ops_manager: MongoDBOpsManager) -> dict:
    return ops_manager.get_om_tester().api_read_license_state()


def expected_ready_condition(om_view: dict, holder_id: str) -> tuple[str, str, str]:
    """The status, reason and message LicenseReady carries for an Ops Manager license view."""
    effective_state = (om_view.get("enforcement") or {}).get("effectiveState", "")
    held = next((entry for entry in om_view.get("licenses", []) if entry["status"] != RELEASED_STATUS), None)
    license = "no license"
    if held is not None:
        license = f"license {held['artifactId']}"
        if held.get("termEnd"):
            license += f", valid until {held['termEnd']}"
    status, reason = "False", "LicenseNotReady"
    state_message = f"License state is {effective_state}."
    if effective_state == "LICENSED":
        status, reason = "True", "LicenseReady"
    if effective_state == "":
        reason, state_message = "LicenseStateUnknown", "Ops Manager has not reported a license state yet."
    if reason == "LicenseStateUnknown":
        status = "Unknown"
    message = f"{state_message} Ops Manager holds {license}."
    if holder_id:
        message += f" Holder ID {holder_id}."
    return status, reason, message


def cr_matches_om_license(ops_manager: MongoDBOpsManager, om_view: dict) -> bool:
    """Whether LicenseReady reports the Ops Manager license view for the current generation."""
    condition = om_condition(ops_manager, LICENSE_READY_CONDITION)
    if condition is None or condition["observedGeneration"] < ops_manager.get_generation():
        return False
    expected = expected_ready_condition(om_view, om_holder_id(ops_manager))
    return (condition["status"], condition["reason"], condition["message"]) == expected


def assert_om_and_appdb_running(ops_manager: MongoDBOpsManager) -> None:
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=60)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=60)


def nudge_reconcile(ops_manager: MongoDBOpsManager) -> None:
    ops_manager.load()
    ops_manager["metadata"].setdefault("annotations", {})["reconcile"] = str(int(time.time()))
    ops_manager.update()


def set_annotation_and_wait_cleared(ops_manager: MongoDBOpsManager, annotation: str) -> None:
    """Sets a one shot annotation and waits for the operator to remove it."""
    ops_manager.load()
    ops_manager["metadata"].setdefault("annotations", {})[annotation] = "true"
    ops_manager.update()

    ops_manager.wait_for(
        lambda om: annotation not in om["metadata"].get("annotations", {}),
        timeout=DEFAULT_TIMEOUT,
    )


def wait_for_cr_license_sync(ops_manager: MongoDBOpsManager, *, installed: Optional[str] = None) -> tuple[dict, dict]:
    """Waits until the current generation is reconciled and LicenseReady reports the Ops Manager view.

    installed is the license from secretRef LicenseManaged must report the operator manages, empty for
    none. None skips the check. Returns the LicenseReady condition and the Ops Manager view.
    """
    generation = generation_of(ops_manager)

    def synced(_: MongoDBOpsManager) -> bool:
        om_view = read_om_license_view(ops_manager)
        in_sync = _managed_as(ops_manager, generation, installed) and cr_matches_om_license(ops_manager, om_view)
        if not in_sync:
            logger.info(
                f"License conditions not in sync yet, CR: {ops_manager['status']['opsManager'].get('conditions')}, "
                f"Ops Manager: {om_view}"
            )
            nudge_reconcile(ops_manager)
        return in_sync

    ops_manager.wait_for(synced, timeout=DEFAULT_TIMEOUT)
    return license_ready_condition(ops_manager), read_om_license_view(ops_manager)
