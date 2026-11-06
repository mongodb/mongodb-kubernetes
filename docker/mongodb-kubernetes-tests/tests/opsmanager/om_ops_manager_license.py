"""Ops Manager license E2E against a connected Ops Manager and the dev EA control plane.

Only behavior that needs a real Ops Manager or EA lives here. The operator's decisions on a given
Ops Manager answer are covered by opsmanager_license_test.go and opsmanager_license_envtest_test.go.
"""

from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.opsmanager.license_e2e_helpers import (
    ENROLLMENT_RECONCILE_CRON,
    LICENSE_SECRET_NAME,
    LICENSE_STATE_EVALUATION_CRON,
    RECOMPUTE_ANNOTATION,
    ROTATE_IDENTITY_ANNOTATION,
    assert_foreign_license_kept,
    assert_license_installed,
    assert_om_license_unchanged,
    assert_secret_absent,
    cr_matches_om_license,
    current_artifact,
    current_artifact_id,
    current_receipt_secret_name,
    delete_secret_if_exists,
    ensure_license_artifact_secret,
    hide_failure_values,
    install_and_release_license,
    install_expired_license,
    install_license,
    install_license_out_of_band,
    installed_artifact_id,
    issue_top_up_and_auto_install,
    license_ready_condition,
    mint_run_license,
    nudge_reconcile,
    om_artifact_ids,
    om_artifact_released,
    om_artifact_usable,
    om_holder_id,
    ops_manager,
    paused_cron,
    prepare_for_license_install,
    read_om_license_view,
    remove_install_intent_without_identity,
    renew_license,
    reset_license_e2e_state,
    revoke_at_ea,
    rotate_identity_and_recompute,
    same_release_receipt,
    set_annotation_and_wait_cleared,
    set_license_install_spec,
    set_license_release_spec,
    set_license_spec,
    set_release_receipts_spec,
    trigger_cron,
    wait_for_cr_license_sync,
    wait_for_ea_artifact_state,
    wait_for_ea_artifact_status,
    wait_for_ea_holder_released,
    wait_for_installed_license,
    wait_for_license_receipt,
    wait_for_license_reconciled_reason,
    wait_for_observed_api_state,
    wait_for_om_artifact_unusable,
)


@mark.e2e_om_ops_manager_license
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_ops_manager_license
class TestOpsManagerLicenseE2E:
    @fixture(autouse=True)
    def _reset_license_state(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        reset_license_e2e_state(ops_manager)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=60)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=60)

    def test_observe_only_license_status(self, ops_manager: MongoDBOpsManager):
        """Without spec.license LicenseReady reports the state and identity Ops Manager reports."""
        ready, _ = wait_for_cr_license_sync(ops_manager, installed="")

        assert ready["status"] == "False"
        assert ready["reason"] == "LicenseGrace"
        assert f"Holder ID {om_holder_id(ops_manager)}." in ready["message"]

    def test_install_and_claim(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The license from the Secret is installed, claimed at EA, and reported in the CR."""
        install_license(ops_manager, namespace)
        om_view = read_om_license_view(ops_manager)

        assert cr_matches_om_license(ops_manager, om_view)
        assert_license_installed(ops_manager, current_artifact_id())
        assert om_view["enforcement"].get("enforced", False) is False
        assert f"Holder ID {om_holder_id(ops_manager)}." in license_ready_condition(ops_manager)["message"]

    def test_recompute_annotation_is_consumed(self, ops_manager: MongoDBOpsManager):
        """The operator removes the recompute annotation and reports the license reconciled."""
        set_license_spec(ops_manager, {})
        set_annotation_and_wait_cleared(ops_manager, RECOMPUTE_ANNOTATION)
        wait_for_cr_license_sync(ops_manager, installed="")

    def test_release_without_receipt(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Removing secretRef releases the license at EA and writes no receipt."""
        install_and_release_license(ops_manager, namespace)

        assert_secret_absent(namespace, current_receipt_secret_name())

    def test_release_after_license_secret_deleted_leaves_license_held(
        self, ops_manager: MongoDBOpsManager, namespace: str
    ):
        """Without the license Secret the operator cannot tell its license from a foreign one, so removing
        secretRef releases nothing, reports why once, and leaves the license to the Ops Manager UI."""
        installed = install_license(ops_manager, namespace)

        delete_secret_if_exists(namespace, LICENSE_SECRET_NAME)
        remove_install_intent_without_identity(ops_manager, installed)

        wait_for_ea_artifact_state(ops_manager, installed, "ACTIVE")

    def test_release_after_license_secret_changed_leaves_license_held(
        self, ops_manager: MongoDBOpsManager, namespace: str
    ):
        """A license Secret that no longer holds a license does not identify the installed one, so removing
        secretRef releases nothing, reports why once, and leaves the license to the Ops Manager UI."""
        installed = install_license(ops_manager, namespace)

        ensure_license_artifact_secret(namespace, "not-a-license")
        wait_for_license_reconciled_reason(ops_manager, "LicenseArtifactInvalid")
        remove_install_intent_without_identity(ops_manager, installed)

        wait_for_ea_artifact_state(ops_manager, installed, "ACTIVE")

    def test_install_release_then_receipt(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The receipt is read from Ops Manager after the release, and written again when its Secret is lost."""
        install_and_release_license(ops_manager, namespace)

        set_release_receipts_spec(ops_manager)
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

    def test_outdated_artifact_in_secret_is_reported_without_touching_om(
        self, ops_manager: MongoDBOpsManager, namespace: str
    ):
        """Ops Manager rejects a license older than one it has seen, and LicenseReconciled reports it as outdated."""
        prepare_for_license_install(ops_manager)
        older_raw, older_id = current_artifact(), current_artifact_id()
        mint_run_license(ops_manager)
        try:
            ensure_license_artifact_secret(namespace, current_artifact())
            set_license_install_spec(ops_manager)
            wait_for_installed_license(ops_manager, current_artifact_id())
            before = read_om_license_view(ops_manager)

            ensure_license_artifact_secret(namespace, older_raw)
            reconciled = wait_for_license_reconciled_reason(ops_manager, "LicenseOutdated")

            assert reconciled["status"] == "False", reconciled["message"]
            assert f"rejected license {older_id}" in reconciled["message"], reconciled["message"]
            assert f"It holds license {current_artifact_id()}" in reconciled["message"], reconciled["message"]
            assert_om_license_unchanged(before, read_om_license_view(ops_manager))
        finally:
            # Ops Manager never held the older license, so only a revoke retires it at EA.
            revoke_at_ea(ops_manager, older_id)

    def test_out_of_band_install_and_release_are_mirrored(self, ops_manager: MongoDBOpsManager):
        """Changes made through the Ops Manager admin API show up in the CR, which never acts on them, not even
        with automatic release receipts and after the rotate identity and recompute annotations."""
        install_license_out_of_band(ops_manager)
        _, om_view = wait_for_cr_license_sync(ops_manager)

        assert cr_matches_om_license(ops_manager, om_view)
        assert not installed_artifact_id(ops_manager)

        set_license_spec(ops_manager, {"releaseReceiptsPolicy": "Automatic"})
        rotate_identity_and_recompute(ops_manager)
        assert_foreign_license_kept(ops_manager, current_artifact_id())

        released = current_artifact_id()
        ops_manager.get_om_tester().api_release_license(released)
        ready, _ = wait_for_observed_api_state(ops_manager, effective_state="GRACE", installed="")

        assert om_artifact_released(ops_manager, released)
        assert ready["reason"] == "LicenseGrace"

    def test_auto_installed_top_up_is_reported_not_overridden(self, ops_manager: MongoDBOpsManager, namespace: str):
        """A top up Ops Manager pulls from EA replaces the license from the Secret. The CR reports it, Ops
        Manager rejects the license from the Secret as outdated, and removing secretRef, rotating the identity
        and recomputing leave the top up alone."""
        original = install_license(ops_manager, namespace)

        top_up = issue_top_up_and_auto_install(ops_manager)
        wait_for_ea_artifact_state(ops_manager, original, "RELEASED")
        reconciled = wait_for_license_reconciled_reason(ops_manager, "LicenseOutdated")
        om_view = read_om_license_view(ops_manager)

        assert reconciled["status"] == "False", reconciled["message"]
        assert f"rejected license {original}" in reconciled["message"], reconciled["message"]
        assert cr_matches_om_license(ops_manager, om_view)
        assert om_artifact_released(ops_manager, original)
        assert top_up in om_artifact_ids(ops_manager)

        set_license_release_spec(ops_manager)
        rotate_identity_and_recompute(ops_manager)

        assert_foreign_license_kept(ops_manager, top_up)

    def test_revoke_at_ea_then_recompute_moves_to_grace(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Once Ops Manager stops using a license revoked at EA, the recompute annotation moves the state to GRACE
        while the evaluation cron is paused. The operator does not PUT the Secret again, and removing secretRef
        reconciles cleanly."""
        revoked = install_license(ops_manager, namespace)

        assert revoke_at_ea(ops_manager, revoked) == "REVOKED"
        wait_for_ea_artifact_status(ops_manager, revoked, "REVOKED")
        with paused_cron(ops_manager, LICENSE_STATE_EVALUATION_CRON):
            wait_for_om_artifact_unusable(ops_manager, revoked)
            assert read_om_license_view(ops_manager)["enforcement"]["effectiveState"] != "GRACE"

            set_annotation_and_wait_cleared(ops_manager, RECOMPUTE_ANNOTATION)
            ready, om_view = wait_for_observed_api_state(ops_manager, effective_state="GRACE", installed=revoked)

        assert cr_matches_om_license(ops_manager, om_view)
        assert ready["reason"] == "LicenseGrace"
        assert not om_artifact_usable(ops_manager, revoked)

        set_license_release_spec(ops_manager)
        wait_for_cr_license_sync(ops_manager, installed="")

    def test_rotate_identity_annotation(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The annotation rotates the enrollment identity, and the claim survives at EA under the same holder."""
        install_license(ops_manager, namespace)
        holder = om_holder_id(ops_manager)

        set_annotation_and_wait_cleared(ops_manager, ROTATE_IDENTITY_ANNOTATION)
        wait_for_cr_license_sync(ops_manager, installed=current_artifact_id())
        trigger_cron(ops_manager, ENROLLMENT_RECONCILE_CRON)

        assert_license_installed(ops_manager, current_artifact_id())
        assert om_artifact_usable(ops_manager, current_artifact_id())
        assert om_holder_id(ops_manager) == holder
        wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "ACTIVE")

    def test_expired_license_is_enforced_then_recovers(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The CR reports an expired license, which Ops Manager enforces, and a fresh one from the Secret recovers."""
        install_expired_license(ops_manager)
        wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "ACTIVE")
        ready, om_view = wait_for_observed_api_state(
            ops_manager, effective_state="EXPIRED", enforced=True, installed=""
        )

        assert cr_matches_om_license(ops_manager, om_view)
        assert ready["reason"] == "LicenseExpired"
        assert om_view["enforcement"]["cause"].split(":")[-1] == "EXPIRED"

        install_license(ops_manager, namespace)

        assert_license_installed(ops_manager, current_artifact_id())

    def test_reset_license_state(self, ops_manager: MongoDBOpsManager):
        """Leaves Ops Manager without a license so the run holds nothing at EA."""
        assert not installed_artifact_id(ops_manager)
