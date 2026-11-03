"""Ops Manager license E2E against a connected Ops Manager and the dev EA control plane.

Only behavior that needs a real Ops Manager or EA lives here. The operator's decisions on a given
Ops Manager answer are covered by opsmanager_license_test.go and opsmanager_license_envtest_test.go.
"""

import pytest
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.opsmanager.license_e2e_helpers import (
    RECOMPUTE_ANNOTATION,
    assert_om_and_appdb_running,
    current_artifact_id,
    ea_artifact_row,
    hide_failure_values,
    mint_run_license,
    om_holder_id,
    ops_manager,
    wait_for_cr_license_sync,
)


@fixture(autouse=True)
def _reset_license_state(ops_manager: MongoDBOpsManager):
    yield
    assert_om_and_appdb_running(ops_manager)


@pytest.mark.e2e_om_ops_manager_license
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_om_ops_manager_license
class TestOpsManagerLicenseE2E:
    def test_observe_only_license_status(self, ops_manager: MongoDBOpsManager):
        """Without spec.license LicenseReady reports the state and identity Ops Manager reports."""
        ready, _ = wait_for_cr_license_sync(ops_manager, installed="")

        assert ready["status"] == "False"
        assert ready["reason"] == "LicenseNotReady"
        assert f"Holder ID {om_holder_id(ops_manager)}." in ready["message"]

    def test_mint_license_for_this_ops_manager(self, ops_manager: MongoDBOpsManager):
        """EA issues a license bound to this Ops Manager, and lists it for the run's principal."""
        artifact_id = mint_run_license(ops_manager)

        assert artifact_id == current_artifact_id()
        assert ea_artifact_row(ops_manager, artifact_id)["status"] == "ISSUED"

    def test_recompute_annotation(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["license"] = {}
        ops_manager["metadata"].setdefault("annotations", {})[RECOMPUTE_ANNOTATION] = "true"
        ops_manager.update()
        ready, _ = wait_for_cr_license_sync(ops_manager)
        assert RECOMPUTE_ANNOTATION not in ops_manager["metadata"].get("annotations", {})

        assert ready["reason"] == "LicenseNotReady"
