"""Ops Manager license E2E with two Ops Managers competing for one single instance license."""

from typing import Optional

import pytest
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.license_e2e_helpers import (
    CLAIM_CONFLICT_CAUSE,
    DENIED_STATUS,
    LICENSE_SECRET_NAME,
    assert_reconcile_failed,
    cr_license_row,
    deploy_license_ops_manager,
    ea_holder_states,
    ensure_license_artifact_secret,
    installed_artifact_id,
    mint_single_instance_license,
    om_holder_id,
    reset_license_e2e_state,
    set_license_install_spec,
    set_license_release_spec,
    wait_for_cr_license_row_status,
    wait_for_cr_license_sync,
    wait_for_ea_artifact_state,
    wait_for_installed_license,
    wait_for_license_ops_manager_running,
    wait_for_license_reconciled,
    wait_for_om_artifact_stays_unusable,
    wait_for_released_artifact,
)

SECOND_LICENSE_SECRET_NAME = "om-license-artifact-second"


@fixture(scope="module")
def first_om(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    return deploy_license_ops_manager(namespace, "om-license-first", custom_version, custom_appdb_version)


@fixture(scope="module")
def second_om(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str, first_om: MongoDBOpsManager
) -> MongoDBOpsManager:
    second = deploy_license_ops_manager(namespace, "om-license-second", custom_version, custom_appdb_version)
    wait_for_license_ops_manager_running(first_om)
    wait_for_license_ops_manager_running(second)
    return second


def install_through_cr(ops_manager: MongoDBOpsManager, raw: str, secret_name: str) -> None:
    ensure_license_artifact_secret(ops_manager.namespace, raw, name=secret_name)
    set_license_install_spec(ops_manager, secret_name=secret_name)


@pytest.mark.e2e_om_ops_manager_license_two_om
class TestOpsManagerLicenseTwoOmE2E:
    def test_claim_conflict_then_handover(self, first_om: MongoDBOpsManager, second_om: MongoDBOpsManager):
        """A license the first Ops Manager holds is denied on the second, which its CR reports. Once the first
        releases it, it is outdated on the second, while a newer license the first releases is then installed
        on the second through its Secret."""
        raw, conflict = mint_single_instance_license(first_om)
        install_through_cr(first_om, raw, LICENSE_SECRET_NAME)
        wait_for_installed_license(first_om, conflict)
        wait_for_ea_artifact_state(first_om, conflict, "ACTIVE")

        install_through_cr(second_om, raw, SECOND_LICENSE_SECRET_NAME)
        wait_for_cr_license_row_status(second_om, conflict, DENIED_STATUS)

        denied = cr_license_row(second_om, conflict)
        assert denied is not None
        assert denied["statusCause"] == CLAIM_CONFLICT_CAUSE
        wait_for_om_artifact_stays_unusable(second_om, conflict)
        holders = ea_holder_states(first_om, conflict)
        assert holders[om_holder_id(first_om)] == "ACTIVE"
        assert holders.get(om_holder_id(second_om)) != "ACTIVE"

        set_license_release_spec(first_om)
        wait_for_released_artifact(first_om, conflict)
        wait_for_ea_artifact_state(first_om, conflict, "RELEASED")
        set_license_release_spec(second_om)
        wait_for_cr_license_sync(second_om, installed="")

        # The denied install moved the second Ops Manager's anti rollback floor to this license.
        install_through_cr(second_om, raw, SECOND_LICENSE_SECRET_NAME)
        assert_reconcile_failed(
            wait_for_license_reconciled(second_om, expected=False), "LicenseInstallFailed", "409", "ARTIFACT_OUTDATED"
        )

        newer_raw, newer = mint_single_instance_license(first_om)
        install_through_cr(first_om, newer_raw, LICENSE_SECRET_NAME)
        wait_for_installed_license(first_om, newer)
        wait_for_ea_artifact_state(first_om, newer, "ACTIVE")
        set_license_release_spec(first_om)
        wait_for_released_artifact(first_om, newer)
        wait_for_ea_artifact_state(first_om, newer, "RELEASED")

        ensure_license_artifact_secret(second_om.namespace, newer_raw, name=SECOND_LICENSE_SECRET_NAME)
        wait_for_installed_license(second_om, newer)
        wait_for_ea_artifact_state(second_om, newer, "ACTIVE")

        assert installed_artifact_id(second_om) == newer

    def test_reset_license_state(self, first_om: MongoDBOpsManager, second_om: MongoDBOpsManager):
        """Leaves both Ops Managers without a license so the run holds nothing at EA."""
        for ops_manager in (first_om, second_om):
            ops_manager.load()
            reset_license_e2e_state(ops_manager)
            assert not installed_artifact_id(ops_manager)
