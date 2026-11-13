"""Leaf certificate renewal on the same CA for the managed Ops Manager HTTPS and AppDB certificates.

The scenario deploys an Ops Manager with a self-signed operator-managed HTTPS certificate and a
self-signed managed AppDB server certificate, without backup. It renews each leaf on its unchanged
CA, the way cert-manager does when a renewal is due:

- the AppDB leaf: the operator writes the new files under a new -pem hash and points the automation
  config certificateKeyFile at it, so the agents restart mongod in place and the AppDB pods are
  not recreated; Ops Manager keeps using the AppDB and monitoring keeps flowing;
- the Ops Manager HTTPS leaf: the PEM hash names the file in mms.https.PEMKeyFile, so the Ops
  Manager pods roll onto the new file and serve the new leaf, still trusted by the unchanged bundle.

The renewal status of both components must follow cert-manager's own Certificate status.
"""

from pathlib import Path

import pytest
from kubetester import read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import run_periodically
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.opsmanager.managedcert_backup_rotation_helper import (
    APP_DB_PORT,
    appdb_member_hosts,
    om_pod_uids,
    ops_manager_endpoint,
    wait_for_om_pem_key_file,
    wait_for_om_pods_replaced,
    wait_for_renewal_status_matching_certificate,
    wait_for_served_leaf,
)
from tests.opsmanager.managedcert_helper import (
    APP_DB_CA_BUNDLE_KEY,
    CA_BUNDLE_KEY,
    ManagedCertificateNames,
    _assert_appdb_tls_scram,
    _wait_for_appdb_automation_config,
    _wait_for_appdb_convergence,
    _wait_for_appdb_pem_secret,
    _write_appdb_ca,
    ca_bundle_snapshot,
    cert_manager,
    default_operator,
    latest_ca_bundle_pem,
    managed_ops_manager,
    pod_uids,
    wait_for_certificates_ready,
    wait_for_om_https_trusted_by_bundle,
)
from tests.tls.managedcert_helper import (
    trigger_certificate_renewal,
    wait_for_certificate_issuance_after,
    wait_for_certificate_issuance_settled,
)

OM_NAME = "om-managedcert-renewal"
APP_DB_MEMBERS = 3


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    return managed_ops_manager(OM_NAME, namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def names(ops_manager: MongoDBOpsManager) -> ManagedCertificateNames:
    return ManagedCertificateNames.for_ops_manager(ops_manager)


def _wait_for_appdb_tls_scram(ops_manager: MongoDBOpsManager, ca_path: str, timeout: int = 600):
    """A verified TLS SCRAM write succeeds, retried while the members restart and elect a primary."""

    def connected() -> tuple[bool, str]:
        try:
            _assert_appdb_tls_scram(ops_manager, ca_path)
        except Exception as exception:
            return False, f"{type(exception).__name__}: {str(exception)[:300]}"
        return True, "connected"

    run_periodically(connected, timeout=timeout, sleep_time=10, msg="a verified TLS SCRAM connection to the AppDB")


@pytest.mark.e2e_om_ops_manager_managedcert_renewal
class TestOpsManagerManagedCertificateRenewal:
    def test_install_operator(self, default_operator: Operator):
        default_operator.wait_for_operator_ready()

    def test_ops_manager_and_appdb_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)

        om_url = ops_manager.om_status().get_url()
        assert om_url is not None
        assert om_url.startswith("https://")

    def test_certificates_ready_and_renewal_status(
        self, ops_manager: MongoDBOpsManager, names: ManagedCertificateNames
    ):
        """Both leaves are reported with the expiry and renewal time cert-manager scheduled."""
        wait_for_certificates_ready(ops_manager)
        for status_field, certificate_name in (
            ("opsManager", names.om_leaf_certificate),
            ("applicationDatabase", names.app_db_leaf_certificate),
        ):
            wait_for_renewal_status_matching_certificate(ops_manager, status_field, certificate_name)

    def test_appdb_monitoring_reaches_ops_manager(self, ops_manager: MongoDBOpsManager):
        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()

    def test_appdb_leaf_renewal_with_same_ca(
        self, ops_manager: MongoDBOpsManager, namespace: str, names: ManagedCertificateNames
    ):
        """The renewed AppDB leaf reaches mongod through the automation config, without new pods."""
        _wait_for_appdb_convergence(ops_manager, APP_DB_MEMBERS)
        revision, pem_hash = wait_for_certificate_issuance_settled(
            namespace, names.app_db_leaf_certificate, names.app_db_pem_secret
        )
        _wait_for_appdb_automation_config(ops_manager, APP_DB_MEMBERS, pem_hash)

        leaf_before = read_secret(namespace, names.app_db_leaf_certificate)["tls.crt"]
        ca = read_secret(namespace, names.app_db_ca_certificate)["tls.crt"]
        appdb_bundle_before = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        om_bundle_before = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        appdb_pods_before = pod_uids(namespace, f"{names.app_db_name}-")
        assert len(appdb_pods_before) == APP_DB_MEMBERS, f"unexpected AppDB pods {appdb_pods_before}"
        renewal_before = wait_for_renewal_status_matching_certificate(
            ops_manager, "applicationDatabase", names.app_db_leaf_certificate
        )

        trigger_certificate_renewal(namespace, names.app_db_leaf_certificate)
        _, new_hash, _ = wait_for_certificate_issuance_after(
            namespace, names.app_db_leaf_certificate, names.app_db_pem_secret, revision, pem_hash
        )
        assert new_hash != pem_hash, "the renewed AppDB leaf must be written under a new -pem hash"

        leaf = read_secret(namespace, names.app_db_leaf_certificate)["tls.crt"]
        assert leaf != leaf_before, "the AppDB leaf must have been renewed"
        assert cert_signed_by_bundle(leaf, ca), "the renewed AppDB leaf must be signed by the same CA"
        assert _wait_for_appdb_pem_secret(namespace, names.app_db_name) == new_hash

        # The automation config names the new certificateKeyFile; the CAFilePath stays on the
        # unchanged bundle hash.
        _wait_for_appdb_automation_config(ops_manager, APP_DB_MEMBERS, new_hash)
        assert (
            ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
            == appdb_bundle_before
        ), "a leaf renewal on the same CA must not republish the AppDB bundle"
        assert read_secret(namespace, names.app_db_ca_certificate)["tls.crt"] == ca, "the AppDB CA must not change"

        # The agents restart mongod onto the new file: every member serves the renewed leaf.
        for host in appdb_member_hosts(ops_manager):
            wait_for_served_leaf(host, APP_DB_PORT, leaf)

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        assert (
            pod_uids(namespace, f"{names.app_db_name}-") == appdb_pods_before
        ), "the AppDB pods must not be recreated for a leaf renewal"
        assert (
            ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY) == om_bundle_before
        ), "renewing the AppDB leaf must not change the Ops Manager bundle"

        wait_for_renewal_status_matching_certificate(
            ops_manager,
            "applicationDatabase",
            names.app_db_leaf_certificate,
            newer_than=renewal_before["notAfter"],
        )

    def test_ops_manager_uses_appdb_after_appdb_renewal(
        self, ops_manager: MongoDBOpsManager, namespace: str, names: ManagedCertificateNames, tmp_path: Path
    ):
        """Clients that trust the unchanged AppDB bundle, Ops Manager included, still connect."""
        _wait_for_appdb_tls_scram(ops_manager, _write_appdb_ca(namespace, tmp_path / "appdb-ca.pem", names.app_db_name))
        wait_for_om_https_trusted_by_bundle(
            ops_manager, latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        )
        # Ops Manager answers these API calls from the AppDB, and the monitoring agents of the
        # restarted members keep sending data.
        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()

    def test_om_leaf_renewal_with_same_ca(
        self, ops_manager: MongoDBOpsManager, namespace: str, names: ManagedCertificateNames
    ):
        """The renewed Ops Manager leaf rolls the Ops Manager pods and is trusted by the same bundle."""
        revision, pem_hash = wait_for_certificate_issuance_settled(
            namespace, names.om_leaf_certificate, names.om_pem_secret
        )
        wait_for_om_pem_key_file(ops_manager, pem_hash)

        leaf_before = read_secret(namespace, names.om_leaf_certificate)["tls.crt"]
        ca = read_secret(namespace, names.om_ca_certificate)["tls.crt"]
        om_bundle_before = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        appdb_bundle_before = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        om_pods_before = om_pod_uids(ops_manager)
        appdb_pods_before = pod_uids(namespace, f"{names.app_db_name}-")
        renewal_before = wait_for_renewal_status_matching_certificate(
            ops_manager, "opsManager", names.om_leaf_certificate
        )

        trigger_certificate_renewal(namespace, names.om_leaf_certificate)
        _, new_hash, _ = wait_for_certificate_issuance_after(
            namespace, names.om_leaf_certificate, names.om_pem_secret, revision, pem_hash
        )
        assert new_hash != pem_hash, "the renewed Ops Manager leaf must be written under a new -pem hash"

        leaf = read_secret(namespace, names.om_leaf_certificate)["tls.crt"]
        assert leaf != leaf_before, "the Ops Manager leaf must have been renewed"
        assert cert_signed_by_bundle(leaf, ca), "the renewed Ops Manager leaf must be signed by the same CA"
        assert leaf in read_secret(namespace, names.om_pem_secret).get(
            new_hash, ""
        ), "the -pem secret must hold the renewed leaf under the new hash"

        # mms.https.PEMKeyFile names the -pem hash, so the change rolls the Ops Manager pods.
        wait_for_om_pem_key_file(ops_manager, new_hash)
        wait_for_om_pods_replaced(ops_manager, om_pods_before)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)

        host, port = ops_manager_endpoint(ops_manager)
        wait_for_served_leaf(host, port, leaf)

        om_bundle_after = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        assert om_bundle_after == om_bundle_before, "a leaf renewal on the same CA must not republish the OM bundle"
        wait_for_om_https_trusted_by_bundle(
            ops_manager, latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        )

        assert (
            ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
            == appdb_bundle_before
        ), "renewing the Ops Manager leaf must not change the AppDB bundle"
        assert (
            pod_uids(namespace, f"{names.app_db_name}-") == appdb_pods_before
        ), "renewing the Ops Manager leaf must not recreate the AppDB pods"

        renewal = wait_for_renewal_status_matching_certificate(
            ops_manager, "opsManager", names.om_leaf_certificate, newer_than=renewal_before["notAfter"]
        )
        assert renewal["notAfter"] != renewal_before["notAfter"]
        wait_for_certificates_ready(ops_manager)

    def test_monitoring_after_om_renewal(self, ops_manager: MongoDBOpsManager):
        """The AppDB agents reach the rolled Ops Manager through the unchanged bundle."""
        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()
