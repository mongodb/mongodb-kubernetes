"""Shared helpers, fixtures, and constants for Ops Manager license E2E tests."""

import time
import uuid
from typing import Optional

import pytest
from kubetester import read_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.conftest import is_multi_cluster
from tests.opsmanager.ea_license import GsaCredentials, artifact_id_of, mint_license, read_artifact_inventory
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

logger = test_logger.get_test_logger(__name__)

OM_NAME = "om-license-e2e"
DEFAULT_TIMEOUT = 90

GSA_SECRET_NAME = "om-license-gsa"


RECOMPUTE_ANNOTATION = "mongodb.com/license-recompute"

# The EA principal every license of this run is minted for, which scopes the inventory reads to this run.
RUN_PRINCIPAL_ID = f"mck-ci-{uuid.uuid4()}"

_current_artifact: Optional[str] = None


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

    if try_load(resource):
        resource.update()
    else:
        resource.create()

    resource.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    resource.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    return resource


def current_artifact() -> str:
    assert _current_artifact is not None, "no license minted yet"
    return _current_artifact


def current_artifact_id() -> str:
    return artifact_id_of(current_artifact())


def gsa_credentials(namespace: str) -> GsaCredentials:
    data = read_secret(namespace, GSA_SECRET_NAME)
    return GsaCredentials(client_id=data["GSA_CLIENT_ID"], client_secret=data["GSA_CLIENT_SECRET"])


def om_holder_id(ops_manager: MongoDBOpsManager) -> str:
    return ops_manager.get_om_tester().api_read_license_identity()["holderId"]


def _mint(ops_manager: MongoDBOpsManager) -> tuple[str, str]:
    """Mints a license for this run, bound to this Ops Manager."""
    return mint_license(
        gsa_credentials(ops_manager.namespace),
        principal_id=RUN_PRINCIPAL_ID,
        holder_ids=[om_holder_id(ops_manager)],
    )


def mint_run_license(ops_manager: MongoDBOpsManager) -> str:
    """Mints a license and makes it the current artifact."""
    global _current_artifact
    _current_artifact, artifact_id = _mint(ops_manager)
    return artifact_id


def ea_artifact_row(ops_manager: MongoDBOpsManager, artifact_id: str) -> dict:
    return read_artifact_inventory(gsa_credentials(ops_manager.namespace), RUN_PRINCIPAL_ID).get(artifact_id) or {}


def license_status(ops_manager: MongoDBOpsManager) -> dict:
    return ops_manager["status"]["opsManager"]["license"]


def license_condition(ops_manager: MongoDBOpsManager, condition_type: str) -> Optional[dict]:
    for condition in ops_manager["status"]["opsManager"]["conditions"]:
        if condition["type"] == condition_type:
            return condition
    return None


def generation_of(ops_manager: MongoDBOpsManager) -> int:
    ops_manager.load()
    return ops_manager["metadata"]["generation"]


def _reconciled_condition(ops_manager: MongoDBOpsManager, generation: int, want: str = "True") -> Optional[dict]:
    """The LicenseReconciled condition, or None while it still describes an older generation.

    Fails at once when the operator settled this generation with a status other than want.
    """
    condition = license_condition(ops_manager, "LicenseReconciled")
    if condition is None or condition["observedGeneration"] < generation:
        return None
    if condition["status"] != want:
        pytest.fail(f"LicenseReconciled is {condition['status']}, expected {want}: {condition['message']}")
    return condition


def read_om_license_view(ops_manager: MongoDBOpsManager) -> dict:
    return ops_manager.get_om_tester().api_read_license_state()


def _omitempty(value):
    """Drops the empty values Go omitempty leaves out of the CR, so both sides compare equal."""
    if isinstance(value, dict):
        kept = {key: _omitempty(item) for key, item in value.items()}
        return {key: item for key, item in kept.items() if item not in (None, False, 0, "", [], {})}
    if isinstance(value, list):
        return [_omitempty(item) for item in value]
    return value


def _mirrored(view: dict) -> dict:
    """The fields the CR mirrors from the Ops Manager license view."""
    return _omitempty(
        {
            "enforcement": view.get("enforcement"),
            "licenses": sorted(view.get("licenses", []), key=lambda entry: entry["artifactId"]),
            "activeEntitlements": view.get("activeEntitlements"),
        }
    )


def cr_matches_om_license(cr: dict, om_view: dict) -> bool:
    return _mirrored(cr) == _mirrored(om_view)


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
    """Waits until the current generation is reconciled and the CR mirrors Ops Manager.

    installed is the installedArtifactId the CR must carry. None skips the check.
    """
    generation = generation_of(ops_manager)

    def synced(_: MongoDBOpsManager) -> bool:
        om_view = read_om_license_view(ops_manager)
        cr = license_status(ops_manager)
        in_sync = (
            _reconciled_condition(ops_manager, generation) is not None
            and cr_matches_om_license(cr, om_view)
            and (installed is None or cr.get("installedArtifactId", "") == installed)
        )
        if not in_sync:
            logger.info(f"License status not in sync yet, CR: {cr}, Ops Manager: {om_view}")
            nudge_reconcile(ops_manager)
        return in_sync

    ops_manager.wait_for(synced, timeout=DEFAULT_TIMEOUT)
    return license_status(ops_manager), read_om_license_view(ops_manager)
