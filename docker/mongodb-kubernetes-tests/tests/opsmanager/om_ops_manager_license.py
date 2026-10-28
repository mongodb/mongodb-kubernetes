"""Ops Manager license E2E against a connected Ops Manager and the dev EA control plane.

Only behavior that needs a real Ops Manager or EA lives here. The operator's decisions on a given
Ops Manager answer are covered by opsmanager_license_test.go and opsmanager_license_envtest_test.go.
"""

import pytest
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.license_e2e_helpers import (
    ENROLLMENT_RECONCILE_CRON,
    RECOMPUTE_ANNOTATION,
    ROTATE_IDENTITY_ANNOTATION,
    assert_license_installed,
    assert_om_and_appdb_running,
    assert_om_license_unchanged,
    assert_reconcile_failed,
    assert_secret_absent,
    cr_matches_om_license,
    current_artifact,
    current_artifact_id,
    current_receipt_secret_name,
    delete_secret_if_exists,
    ea_artifact_row,
    ensure_license_artifact_secret,
    install_and_release_license,
    install_expired_license,
    install_license,
    install_license_out_of_band,
    installed_artifact_id,
    issue_top_up_and_auto_install,
    license_status,
    mint_run_license,
    nudge_reconcile,
    om_artifact_ids,
    om_artifact_released,
    om_artifact_usable,
    om_holder_id,
    ops_manager,
    prepare_for_license_install,
    read_om_license_view,
    renew_license,
    reset_license_e2e_state,
    revoke_at_ea,
    same_release_receipt,
    set_annotation_and_wait_cleared,
    set_license_install_spec,
    set_license_release_spec,
    set_release_receipts_spec,
    trigger_cron,
    wait_for_cr_license_sync,
    wait_for_ea_artifact_state,
    wait_for_ea_artifact_status,
    wait_for_ea_holder_released,
    wait_for_installed_license,
    wait_for_license_receipt,
    wait_for_license_reconcile_failure,
    wait_for_license_reconciled,
    wait_for_observed_api_state,
    wait_for_om_artifact_unusable,
    wait_for_om_effective_state,
)


@fixture(autouse=True)
def _reset_license_state(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    reset_license_e2e_state(ops_manager)
    assert_om_and_appdb_running(ops_manager)


@pytest.mark.e2e_om_ops_manager_license
class TestOpsManagerLicenseE2E:
    def test_observe_only_license_status(self, ops_manager: MongoDBOpsManager):
        """Without spec.license the CR mirrors the state and identity Ops Manager reports."""
        cr, _ = wait_for_cr_license_sync(ops_manager, installed="")

        assert cr["enforcement"]["effectiveState"] == "GRACE"
        assert cr["identity"]["holderId"] == om_holder_id(ops_manager)

    def test_mint_license_for_this_ops_manager(self, ops_manager: MongoDBOpsManager):
        """EA issues a license bound to this Ops Manager, and lists it for the run's principal."""
        artifact_id = mint_run_license(ops_manager)

        assert artifact_id == current_artifact_id()
        assert ea_artifact_row(ops_manager, artifact_id)["status"] == "ISSUED"

    def test_install_and_claim(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The license from the Secret is installed, claimed at EA, and mirrored in the CR."""
        install_license(ops_manager, namespace)
        cr = license_status(ops_manager)

        assert cr_matches_om_license(cr, read_om_license_view(ops_manager))
        assert cr["enforcement"]["effectiveState"] == "LICENSED"
        assert cr["enforcement"].get("enforced", False) is False
        assert cr["identity"]["holderId"] == om_holder_id(ops_manager)

    def test_recompute_annotation(self, ops_manager: MongoDBOpsManager):
        set_annotation_and_wait_cleared(ops_manager, RECOMPUTE_ANNOTATION)
        cr, _ = wait_for_cr_license_sync(ops_manager)

        assert cr["enforcement"]["effectiveState"] == "GRACE"

    def test_release_without_receipt(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Removing licenseSecretRef releases the license at EA and writes no receipt."""
        install_and_release_license(ops_manager, namespace)

        assert_secret_absent(namespace, current_receipt_secret_name())

    def test_install_release_then_receipt(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The receipt is read from Ops Manager after the release, and written again when its Secret is lost."""
        install_and_release_license(ops_manager, namespace)

        set_release_receipts_spec(ops_manager, current_artifact_id())
        receipt = wait_for_license_receipt(ops_manager, current_receipt_secret_name())
        delete_secret_if_exists(namespace, current_receipt_secret_name())
        nudge_reconcile(ops_manager)

        assert same_release_receipt(receipt, wait_for_license_receipt(ops_manager, current_receipt_secret_name()))

    def test_auto_release_receipt_on_renewal(self, ops_manager: MongoDBOpsManager, namespace: str):
        """A renewal through the Secret releases the replaced license at EA and writes its receipt."""
        install_license(ops_manager, namespace, auto_release_receipts=True)
        replaced = current_artifact_id()
        holder = om_holder_id(ops_manager)

        renew_license(ops_manager, namespace)
        wait_for_ea_holder_released(ops_manager, replaced, holder)

        assert wait_for_license_receipt(ops_manager, current_receipt_secret_name())

    def test_outdated_artifact_in_secret_fails_without_touching_om(
        self, ops_manager: MongoDBOpsManager, namespace: str
    ):
        """Ops Manager rejects a license older than one it has seen, and the CR reports the 409."""
        prepare_for_license_install(ops_manager)
        older_raw, older_id = current_artifact(), current_artifact_id()
        mint_run_license(ops_manager)
        try:
            ensure_license_artifact_secret(namespace, current_artifact())
            set_license_install_spec(ops_manager)
            wait_for_installed_license(ops_manager, current_artifact_id())
            before = read_om_license_view(ops_manager)

            ensure_license_artifact_secret(namespace, older_raw)
            reconciled = wait_for_license_reconcile_failure(ops_manager)

            assert_reconcile_failed(
                reconciled, "LicenseInstallFailed", f"rejected license {older_id}", "409", "ARTIFACT_OUTDATED"
            )
            assert_om_license_unchanged(before, read_om_license_view(ops_manager))
            assert installed_artifact_id(ops_manager) == current_artifact_id()
        finally:
            # Ops Manager never held the older license, so only a revoke retires it at EA.
            revoke_at_ea(ops_manager, older_id)

    def test_out_of_band_install_and_release_are_mirrored(self, ops_manager: MongoDBOpsManager):
        """Changes made through the Ops Manager admin API show up in the CR, which never acts on them."""
        install_license_out_of_band(ops_manager)
        cr, om_view = wait_for_cr_license_sync(ops_manager)

        assert cr_matches_om_license(cr, om_view)
        assert not installed_artifact_id(ops_manager)

        released = current_artifact_id()
        ops_manager.get_om_tester().api_release_license(released)
        cr, _ = wait_for_observed_api_state(ops_manager, effective_state="GRACE", installed="")

        assert om_artifact_released(ops_manager, released)
        assert cr["enforcement"]["effectiveState"] == "GRACE"

    def test_auto_installed_top_up_is_mirrored_not_overridden(self, ops_manager: MongoDBOpsManager, namespace: str):
        """A top up Ops Manager pulls from EA replaces the license from the Secret. The CR mirrors it, the
        operator does not put its own license back, and removing licenseSecretRef leaves the top up alone."""
        original = install_license(ops_manager, namespace)

        top_up = issue_top_up_and_auto_install(ops_manager)
        wait_for_ea_artifact_state(ops_manager, original, "RELEASED")
        nudge_reconcile(ops_manager)
        cr, om_view = wait_for_cr_license_sync(ops_manager, installed=original)

        assert cr_matches_om_license(cr, om_view)
        assert om_artifact_released(ops_manager, original)
        assert top_up in om_artifact_ids(ops_manager)

        set_license_release_spec(ops_manager)
        wait_for_cr_license_sync(ops_manager, installed="")

        assert top_up in om_artifact_ids(ops_manager)

    def test_revoke_at_ea_is_mirrored_not_reinstalled(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Once Ops Manager stops using a license revoked at EA, the CR mirrors GRACE, the operator does not PUT the
        Secret again, and removing licenseSecretRef reconciles cleanly."""
        revoked = install_license(ops_manager, namespace)

        assert revoke_at_ea(ops_manager, revoked) == "REVOKED"
        wait_for_ea_artifact_status(ops_manager, revoked, "REVOKED")
        wait_for_om_artifact_unusable(ops_manager, revoked)
        wait_for_om_effective_state(ops_manager, "GRACE")
        nudge_reconcile(ops_manager)
        cr, om_view = wait_for_cr_license_sync(ops_manager, installed=revoked)

        assert cr_matches_om_license(cr, om_view)
        assert cr["enforcement"]["effectiveState"] == "GRACE"
        assert not om_artifact_usable(ops_manager, revoked)

        set_license_release_spec(ops_manager)
        wait_for_cr_license_sync(ops_manager, installed="")

    def test_rotate_identity_annotation(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The annotation rotates the enrollment identity, and the claim survives at EA under the same holder."""
        install_license(ops_manager, namespace)
        holder = om_holder_id(ops_manager)

        set_annotation_and_wait_cleared(ops_manager, ROTATE_IDENTITY_ANNOTATION)
        wait_for_license_reconciled(ops_manager, expected=True)
        trigger_cron(ops_manager, ENROLLMENT_RECONCILE_CRON)

        assert_license_installed(ops_manager, current_artifact_id())
        assert om_artifact_usable(ops_manager, current_artifact_id())
        assert om_holder_id(ops_manager) == holder
        wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "ACTIVE")

    def test_expired_license_is_enforced_then_recovers(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The CR reports an expired license as enforced, and a fresh one from the Secret recovers."""
        install_expired_license(ops_manager)
        wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "ACTIVE")
        cr, om_view = wait_for_observed_api_state(ops_manager, effective_state="EXPIRED", enforced=True, installed="")

        assert cr_matches_om_license(cr, om_view)
        assert cr["enforcement"]["cause"].split(":")[-1] == "EXPIRED"

        install_license(ops_manager, namespace)

        assert_license_installed(ops_manager, current_artifact_id())

    def test_reset_license_state(self, ops_manager: MongoDBOpsManager):
        """Leaves Ops Manager without a license so the run holds nothing at EA."""
        assert not installed_artifact_id(ops_manager)
