"""Ops Manager license E2E against a connected Ops Manager and the dev EA control plane.

Only behavior that needs a real Ops Manager or EA lives here. The operator's decisions on a given
Ops Manager answer are covered by opsmanager_license_test.go and opsmanager_license_envtest_test.go.
"""

import pytest
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.license_e2e_helpers import (
    RECOMPUTE_ANNOTATION,
    assert_om_and_appdb_running,
    om_holder_id,
    ops_manager,
    set_annotation_and_wait_cleared,
    wait_for_cr_license_sync,
)


@fixture(autouse=True)
def _reset_license_state(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    assert_om_and_appdb_running(ops_manager)


@pytest.mark.e2e_om_ops_manager_license
class TestOpsManagerLicenseE2E:
    def test_observe_only_license_status(self, ops_manager: MongoDBOpsManager):
        """Without spec.license the CR mirrors the state and identity Ops Manager reports."""
        cr, _ = wait_for_cr_license_sync(ops_manager, installed="")

        assert cr["enforcement"]["effectiveState"] == "GRACE"
        assert cr["identity"]["holderId"] == om_holder_id(ops_manager)

    def test_recompute_annotation(self, ops_manager: MongoDBOpsManager):
        set_annotation_and_wait_cleared(ops_manager, RECOMPUTE_ANNOTATION)
        cr, _ = wait_for_cr_license_sync(ops_manager)

        assert cr["enforcement"]["effectiveState"] == "GRACE"
