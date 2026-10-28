"""Ops Manager license E2E for an Ops Manager that cannot reach EA, released by receipt."""

from typing import Optional

import pytest
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.ea_license import submit_release_receipt
from tests.opsmanager.license_e2e_helpers import (
    current_artifact,
    current_artifact_id,
    current_receipt_secret_name,
    deploy_license_ops_manager,
    ea_artifact_row,
    ensure_license_artifact_secret,
    gsa_credentials,
    mint_run_license,
    om_holder_id,
    reset_license_e2e_state,
    set_license_install_spec,
    set_release_receipts_spec,
    tampered_receipt,
    wait_for_ea_artifact_status,
    wait_for_installed_license,
    wait_for_license_ops_manager_running,
    wait_for_license_receipt,
    wait_for_released_artifact,
)

# Port 9 is the discard port, nothing listens on it, so every call Ops Manager makes to EA fails.
CLOSED_PROXY = {"http.proxy.host": "127.0.0.1", "http.proxy.port": "9"}


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = deploy_license_ops_manager(
        namespace, "om-license-air-gapped", custom_version, custom_appdb_version, CLOSED_PROXY
    )
    wait_for_license_ops_manager_running(resource)
    return resource


@pytest.mark.e2e_om_ops_manager_license_air_gapped
class TestOpsManagerLicenseAirGappedE2E:
    def test_release_by_receipt(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The license works without an EA claim, and the receipt the operator writes on release is the one
        EA accepts to retire it. A replay reports the original release, and a tampered copy is rejected."""
        ops_manager.load()
        mint_run_license(ops_manager)
        ensure_license_artifact_secret(namespace, current_artifact())
        set_license_install_spec(ops_manager)
        wait_for_installed_license(ops_manager, current_artifact_id())

        row = ea_artifact_row(ops_manager, current_artifact_id())
        assert row["status"] == "ISSUED"
        assert not row.get("holders")

        set_release_receipts_spec(ops_manager, current_artifact_id())
        wait_for_released_artifact(ops_manager, current_artifact_id())
        receipt = wait_for_license_receipt(ops_manager, current_receipt_secret_name())
        credentials = gsa_credentials(namespace)

        first = submit_release_receipt(credentials, receipt)
        assert first.status_code == 200, f"EA answered {first.status_code} to the release receipt"
        assert first.json()["status"] == "RELEASED"
        assert first.json()["artifact_id"] == current_artifact_id()
        assert first.json()["holder_id"] == om_holder_id(ops_manager)
        wait_for_ea_artifact_status(ops_manager, current_artifact_id(), "RELEASED")

        replay = submit_release_receipt(credentials, receipt)
        assert replay.json()["status"] == "ALREADY_RELEASED"
        assert replay.json()["released_at"] == first.json()["released_at"]

        assert submit_release_receipt(credentials, tampered_receipt(receipt)).status_code in (401, 403)

    def test_reset_license_state(self, ops_manager: MongoDBOpsManager):
        """Leaves Ops Manager without a license so the run holds nothing at EA."""
        ops_manager.load()
        reset_license_e2e_state(ops_manager)
