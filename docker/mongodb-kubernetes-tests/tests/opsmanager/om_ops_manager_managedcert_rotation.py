"""Safe self-signed CA rotation for the Ops Manager components with managed certificates.

The scenario deploys an Ops Manager with a self-signed operator-managed OM HTTPS
certificate and a self-signed managed internal AppDB certificate. After reaching a
steady state it rotates the AppDB self-signed CA, waits for the leaf to be re-issued
on the new root, prunes the retained previous CA, then repeats the same cycle for
the Ops Manager HTTPS certificate.

Queryable backup certificates are left out because their fixtures (backing databases,
snapshot stores) are expensive to provision for this module. The backup code path
also follows the same EnsureServerCertificateAndCA function, so the OM HTTPS test
exercises the cycle on a server-only managed certificate.
"""

from datetime import datetime

import pytest
from kubetester import read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.opsmanager.managedcert_helper import (
    APP_DB_CA_BUNDLE_KEY,
    APP_DB_CA_DIR,
    CA_BUNDLE_KEY,
    PREVIOUS_SERVER_CA_RETAINED,
    ManagedCertificateNames,
    begin_ca_rotation,
    bundle_fingerprints,
    ca_bundle_snapshot,
    cert_manager,
    default_operator,
    latest_ca_bundle_pem,
    managed_ops_manager,
    pod_uids,
    request_prune_previous_ca,
    wait_for_appdb_ca_file_path,
    wait_for_bundle_pruned,
    wait_for_bundle_rotation,
    wait_for_ca_rotated,
    wait_for_certificates_ready,
    wait_for_component_certificate_renewal,
    wait_for_component_certificates_ready_message,
    wait_for_leaf_reissued_on_new_ca,
    wait_for_om_https_trusted_by_bundle,
    wait_for_prune_annotation_removed,
)
from tests.tls.managedcert_helper import pem_secret_hashes, read_certificate_status

OM_NAME = "om-managed-cert-rotation"


def _as_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    return managed_ops_manager(OM_NAME, namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def rotation() -> dict:
    """The CA and bundle state each step of the rotation needs to hand to the next one."""
    return {}


@pytest.mark.e2e_om_ops_manager_managedcert_rotation
class TestOpsManagerManagedCertificateRotation:
    def test_install_operator(self, default_operator: Operator):
        default_operator.wait_for_operator_ready()

    def test_ops_manager_and_appdb_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)

    def test_certificates_ready_and_renewal_status(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The deployment reaches a steady state with CertificatesReady=True and renewal times.

        Both the Ops Manager HTTPS leaf certificate and the AppDB leaf certificate are
        reported with their name, expiry, and the next automatic renewal time.
        """
        wait_for_certificates_ready(ops_manager)
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        for status_field, certificate_name in (
            ("opsManager", names.om_leaf_certificate),
            ("applicationDatabase", names.app_db_leaf_certificate),
        ):
            renewal = wait_for_component_certificate_renewal(ops_manager, status_field, certificate_name)
            assert renewal["name"] == certificate_name
            assert renewal.get("notAfter"), f"{status_field} renewal has no notAfter"
            assert renewal.get("renewalTime"), f"{status_field} renewal has no renewalTime"

            cert_status = read_certificate_status(namespace, certificate_name)
            assert cert_status.get("renewalTime"), f"cert-manager has not scheduled the renewal of {certificate_name}"
            assert _as_time(renewal["renewalTime"]) == _as_time(
                cert_status["renewalTime"]
            ), f"the reported {status_field} renewalTime must match cert-manager's own schedule"

    def test_appdb_ca_rotation_publishes_widened_bundle(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        """Rotating the AppDB self-signed CA widens the bundle and reports the retention.

        The operator keeps the previous CA in the bundle so that mongod still trusts
        the existing leaf certificate while it is re-issued on the new root.
        """
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        # Record state before the rotation.
        rotation["appdb_pod_uids"] = pod_uids(namespace, f"{names.app_db_name}-")
        assert rotation["appdb_pod_uids"], "AppDB pods must exist before the CA rotation"

        rotation["appdb_leaf"] = read_secret(namespace, names.app_db_leaf_certificate)["tls.crt"]
        rotation["appdb_leaf_revision"] = read_certificate_status(namespace, names.app_db_leaf_certificate).get(
            "revision", 0
        )
        rotation["appdb_pem_hash"] = pem_secret_hashes(namespace, names.app_db_pem_secret)[0]
        rotation["appdb_bundle"] = ca_bundle_snapshot(
            namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY
        )
        assert rotation["appdb_bundle"]["latest_hash"], "AppDB bundle must have a latestHash"
        assert rotation["appdb_bundle"]["published_at"], "AppDB bundle must have a published-at annotation"

        # Trigger the CA rotation and wait for the new CA to appear.
        rotation["appdb_old_ca"] = begin_ca_rotation(namespace, names.app_db_ca_certificate)
        rotation["appdb_new_ca"] = wait_for_ca_rotated(namespace, names.app_db_ca_certificate, rotation["appdb_old_ca"])

        # The bundle must widen: hold both the old and new CA, set previousHash and
        # update the published-at annotation.
        widened = wait_for_bundle_rotation(
            namespace,
            names.app_db_ca_bundle_config_map,
            APP_DB_CA_BUNDLE_KEY,
            rotation["appdb_bundle"]["latest_hash"],
            rotation["appdb_bundle"]["published_at"],
        )
        rotation["appdb_widened"] = widened
        expected_certificates = set(bundle_fingerprints(rotation["appdb_old_ca"])) | set(
            bundle_fingerprints(rotation["appdb_new_ca"])
        )
        assert (
            set(widened["certificates"]) == expected_certificates
        ), "the widened bundle must hold both the old and the new CA"
        assert len(widened["certificates"]) == 2, "the bundle must have exactly 2 certificates before pruning"

        # The automation config CAFilePath must point at the new bundle hash.
        automation_config = wait_for_appdb_ca_file_path(ops_manager, widened["latest_hash"])
        assert (
            automation_config["tls"]["CAFilePath"] == f"{APP_DB_CA_DIR}/{widened['latest_hash']}"
        ), "the AppDB automation config must name the new CA bundle hash"

        # CertificatesReady must report that the previous server CA is retained.
        wait_for_component_certificates_ready_message(ops_manager, "AppDB", PREVIOUS_SERVER_CA_RETAINED)

    def test_appdb_leaf_is_reissued_on_new_ca(self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict):
        """The operator re-issues the AppDB leaf on the new CA without recreating pods.

        After the widened bundle has been published long enough the operator sets
        Issuing=True with reason ManagedCARotated on the leaf Certificate. Once
        cert-manager issues the new leaf the operator rolls the files into the -pem
        secret and the agents restart mongod.
        """
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        leaf, pem_hash = wait_for_leaf_reissued_on_new_ca(
            namespace,
            names.app_db_leaf_certificate,
            names.app_db_pem_secret,
            rotation["appdb_leaf"],
            rotation["appdb_pem_hash"],
            rotation["appdb_new_ca"],
            timeout=1800,
        )
        revision = read_certificate_status(namespace, names.app_db_leaf_certificate).get("revision", 0)
        assert revision > rotation["appdb_leaf_revision"], "the leaf Certificate revision must have increased"
        assert cert_signed_by_bundle(leaf, rotation["appdb_new_ca"]), "the reissued leaf must be signed by the new CA"
        assert not cert_signed_by_bundle(
            leaf, rotation["appdb_old_ca"]
        ), "the reissued leaf must not be signed by the old CA"
        assert pem_hash != rotation["appdb_pem_hash"], "the PEM secret hash must have changed"
        rotation["appdb_leaf"] = leaf
        rotation["appdb_pem_hash"] = pem_hash

        # The AppDB pods must not have been recreated (just mongod restarted).
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        assert (
            pod_uids(namespace, f"{names.app_db_name}-") == rotation["appdb_pod_uids"]
        ), "the AppDB pods must not be recreated after a CA rotation"

        # OM monitoring of the AppDB must still work.
        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()

        # The bundle must still hold both CAs (not yet pruned).
        snapshot = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        assert len(snapshot["certificates"]) == 2, "the bundle must still hold 2 certificates before pruning"

    def test_prune_appdb_previous_ca(self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict):
        """Pruning the AppDB retained CA drops the old root from the bundle.

        Once every leaf is signed by the new CA the operator accepts the prune
        annotation for that category: it removes the old CA from the bundle, drops
        installs only the new CA (previousHash then names the widened bundle), and removes the annotation.
        The Ops Manager bundle is untouched.
        """
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        om_bundle_before = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)

        request_prune_previous_ca(ops_manager, "appdb")
        wait_for_prune_annotation_removed(ops_manager)

        pruned = wait_for_bundle_pruned(
            namespace,
            names.app_db_ca_bundle_config_map,
            APP_DB_CA_BUNDLE_KEY,
            rotation["appdb_new_ca"],
        )
        assert set(pruned["certificates"]) == set(
            bundle_fingerprints(rotation["appdb_new_ca"])
        ), "the pruned bundle must contain only the new CA"

        om_bundle_after = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        assert om_bundle_after == om_bundle_before, "pruning the AppDB bundle must not change the Ops Manager bundle"

        wait_for_component_certificates_ready_message(
            ops_manager, "AppDB", PREVIOUS_SERVER_CA_RETAINED, present=False, timeout=900
        )

    def test_om_ca_rotation_publishes_widened_bundle(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        """Rotating the Ops Manager self-signed CA widens its bundle and reports retention.

        The same retention logic applies to the Ops Manager server certificate: the
        bundle keeps both CAs until a prune is requested, and the leaf is re-issued
        on the new root.
        """
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        rotation["om_leaf"] = read_secret(namespace, names.om_leaf_certificate)["tls.crt"]
        rotation["om_leaf_revision"] = read_certificate_status(namespace, names.om_leaf_certificate).get("revision", 0)
        rotation["om_pem_hash"] = pem_secret_hashes(namespace, names.om_pem_secret)[0]
        rotation["om_bundle"] = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        assert rotation["om_bundle"]["latest_hash"], "OM bundle must have a latestHash"
        assert rotation["om_bundle"]["published_at"], "OM bundle must have a published-at annotation"

        rotation["om_old_ca"] = begin_ca_rotation(namespace, names.om_ca_certificate)
        rotation["om_new_ca"] = wait_for_ca_rotated(namespace, names.om_ca_certificate, rotation["om_old_ca"])

        widened = wait_for_bundle_rotation(
            namespace,
            names.om_ca_bundle_config_map,
            CA_BUNDLE_KEY,
            rotation["om_bundle"]["latest_hash"],
            rotation["om_bundle"]["published_at"],
        )
        rotation["om_widened"] = widened
        expected_om_certs = set(bundle_fingerprints(rotation["om_old_ca"])) | set(
            bundle_fingerprints(rotation["om_new_ca"])
        )
        assert (
            set(widened["certificates"]) == expected_om_certs
        ), "the widened OM bundle must hold both the old and the new CA"
        assert len(widened["certificates"]) == 2

        # Ops Manager remains reachable over HTTPS with the widened bundle (it still
        # trusts the old leaf until the leaf is re-issued).
        widended_bundle_pem = latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        wait_for_om_https_trusted_by_bundle(ops_manager, widended_bundle_pem, timeout=900)
        wait_for_component_certificates_ready_message(ops_manager, "OpsManager", PREVIOUS_SERVER_CA_RETAINED)

    def test_om_leaf_is_reissued_on_new_ca(self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict):
        """The operator re-issues the Ops Manager leaf on the new CA.

        The Ops Manager StatefulSet is rolled (the pod may be recreated) and the
        health endpoint becomes reachable again.
        """
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        leaf, pem_hash = wait_for_leaf_reissued_on_new_ca(
            namespace,
            names.om_leaf_certificate,
            names.om_pem_secret,
            rotation["om_leaf"],
            rotation["om_pem_hash"],
            rotation["om_new_ca"],
            timeout=1800,
        )
        revision = read_certificate_status(namespace, names.om_leaf_certificate).get("revision", 0)
        assert revision > rotation["om_leaf_revision"], "the OM leaf Certificate revision must have increased"
        assert cert_signed_by_bundle(leaf, rotation["om_new_ca"]), "the reissued OM leaf must be signed by the new CA"
        assert not cert_signed_by_bundle(
            leaf, rotation["om_old_ca"]
        ), "the reissued OM leaf must not be signed by the old CA"
        assert pem_hash != rotation["om_pem_hash"]

        rotation["om_leaf"] = leaf
        rotation["om_pem_hash"] = pem_hash

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)

        widened_bundle_pem = latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        wait_for_om_https_trusted_by_bundle(ops_manager, widened_bundle_pem, timeout=1800)

    def test_prune_om_previous_ca(self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict):
        """Pruning the OM retained CA drops the old root and the Ops Manager stays reachable."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        appdb_bundle_before = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)

        request_prune_previous_ca(ops_manager, "om")
        wait_for_prune_annotation_removed(ops_manager)

        pruned = wait_for_bundle_pruned(
            namespace,
            names.om_ca_bundle_config_map,
            CA_BUNDLE_KEY,
            rotation["om_new_ca"],
        )
        assert set(pruned["certificates"]) == set(
            bundle_fingerprints(rotation["om_new_ca"])
        ), "the pruned OM bundle must contain only the new CA"

        appdb_bundle_after = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        assert appdb_bundle_after == appdb_bundle_before, "pruning the OM bundle must not change the AppDB bundle"

        # The Ops Manager must serve its leaf on the new CA, trusted by the pruned bundle.
        pruned_bundle_pem = latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        wait_for_om_https_trusted_by_bundle(ops_manager, pruned_bundle_pem, timeout=1800)

        wait_for_component_certificates_ready_message(
            ops_manager, "OpsManager", PREVIOUS_SERVER_CA_RETAINED, present=False, timeout=900
        )
        wait_for_certificates_ready(ops_manager)
