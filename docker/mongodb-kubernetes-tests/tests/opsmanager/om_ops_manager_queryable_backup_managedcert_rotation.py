"""Renewal and safe self-signed CA rotation of the managed queryable backup certificate.

The scenario deploys an Ops Manager with backup enabled and spec.backup.managedCertificate: {},
so the operator issues the queryable backup leaf from its own self-signed CA and derives the
queryable.pem Secret (leaf, key and the CA bundle) that Ops Manager and the backup daemon mount.

After taking a snapshot it:
- renews the leaf on the same CA, and checks queryable.pem and the snapshot query follow;
- rotates the self-signed CA, and checks the bundle and queryable.pem keep the previous CA while
  the old leaf is still served, without touching the Ops Manager or AppDB bundles;
- waits for the operator to re-issue the leaf on the new CA, and checks the query is then trusted
  by the new CA alone and no longer by the old one;
- prunes the previous CA and checks the query still works with the pruned bundle.
"""

from collections.abc import Iterator

import pytest
from kubetester import read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.opsmanager.managedcert_backup_rotation_helper import (
    BackupCertificateNames,
    assert_issued_after_bundle_propagation,
    assert_snapshot_query_rejected,
    backup_ca_bundle,
    leaf_issuance,
    other_component_bundles,
    queryable_backup_ops_manager,
    queryable_pem,
    unrelated_ca_pem,
    wait_for_leaf_reissued,
    wait_for_queryable_pem,
    wait_for_renewal_status_matching_certificate,
    wait_for_snapshot_query,
)
from tests.opsmanager.managedcert_helper import (
    CA_BUNDLE_KEY,
    PREVIOUS_SERVER_CA_RETAINED,
    begin_ca_rotation,
    bundle_fingerprints,
    ca_bundle_snapshot,
    cert_manager,
    default_operator,
    request_prune_previous_ca,
    wait_for_bundle_pruned,
    wait_for_bundle_rotation,
    wait_for_ca_rotated,
    wait_for_certificates_ready,
    wait_for_component_certificates_ready_message,
    wait_for_prune_annotation_removed,
)
from tests.opsmanager.om_ops_manager_queryable_backup_managedcert import (
    COLLECTION,
    DATABASE,
    DOCUMENT,
    backed_up_mongodb,
    backing_databases,
)
from tests.tls.managedcert_helper import trigger_certificate_renewal


@fixture(scope="module")
def certificate_mode() -> str:
    # Names the backing databases, the backed up replica set and its project.
    return "rotation"


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    certificate_mode: str,
) -> Iterator[MongoDBOpsManager]:
    resource = queryable_backup_ops_manager(
        f"om-queryable-{certificate_mode}",
        namespace,
        custom_version,
        custom_appdb_version,
        oplog_store=f"queryable-oplog-{certificate_mode}",
        block_store=f"queryable-block-{certificate_mode}",
        managed_certificate={},
    )
    yield resource
    resource.delete()


@fixture(scope="module")
def names(ops_manager: MongoDBOpsManager) -> BackupCertificateNames:
    return BackupCertificateNames(ops_manager.name)


@fixture(scope="module")
def project_name(certificate_mode: str) -> str:
    return f"queryable-data-{certificate_mode}"


@fixture(scope="module")
def rotation() -> dict:
    """The certificate and bundle state each step hands to the next one."""
    return {}


def _query(ops_manager: MongoDBOpsManager, project_name: str, backed_up_mongodb: MongoDB, ca_pem: str | None):
    wait_for_snapshot_query(
        ops_manager, project_name, backed_up_mongodb.name, DATABASE, COLLECTION, DOCUMENT, ca_pem=ca_pem
    )


def _assert_query_rejected(ops_manager: MongoDBOpsManager, project_name: str, backed_up_mongodb: MongoDB, ca_pem: str):
    assert_snapshot_query_rejected(ops_manager, project_name, backed_up_mongodb.name, DATABASE, COLLECTION, ca_pem)


@pytest.mark.e2e_om_ops_manager_queryable_backup_managedcert_rotation
class TestQueryableBackupManagedCertificateRotation:
    def test_install_operator(self, default_operator: Operator):
        default_operator.wait_for_operator_ready()

    def test_ops_manager_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)

    def test_backing_databases(self, ops_manager: MongoDBOpsManager, backing_databases: list[MongoDB]):
        for resource in backing_databases:
            resource.assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_backup_certificate_ready_and_renewal_status(
        self, ops_manager: MongoDBOpsManager, namespace: str, names: BackupCertificateNames
    ):
        """The backup leaf is reported in status.backup.certificateRenewals with cert-manager's schedule."""
        wait_for_certificates_ready(ops_manager)
        wait_for_renewal_status_matching_certificate(ops_manager, "backup", names.leaf_certificate)

        bundle = ca_bundle_snapshot(namespace, names.ca_bundle_config_map, CA_BUNDLE_KEY)
        assert bundle["latest_hash"], "the backup bundle must have a latestHash"
        assert not bundle["previous_hash"], "the backup bundle must not retain a previous CA before any rotation"
        assert bundle["published_at"], "the backup bundle must have a published-at annotation"
        ca = read_secret(namespace, names.ca_certificate)["tls.crt"]
        assert bundle["certificates"] == bundle_fingerprints(ca), "the backup bundle must hold only the backup CA"
        wait_for_queryable_pem(namespace, names)

    def test_query_snapshot(
        self,
        ops_manager: MongoDBOpsManager,
        backed_up_mongodb: MongoDB,
        project_name: str,
        namespace: str,
        names: BackupCertificateNames,
    ):
        backed_up_mongodb.assert_reaches_phase(Phase.Running, timeout=900)
        with backed_up_mongodb.tester().client as client:
            client[DATABASE][COLLECTION].replace_one({"_id": DOCUMENT["_id"]}, DOCUMENT, upsert=True)
        backed_up_mongodb.configure_backup(mode="enabled")
        backed_up_mongodb.update()
        ops_manager.get_om_tester(project_name=project_name).wait_until_backup_snapshots_are_ready(expected_count=1)

        _query(ops_manager, project_name, backed_up_mongodb, backup_ca_bundle(namespace, names))
        _assert_query_rejected(ops_manager, project_name, backed_up_mongodb, unrelated_ca_pem())

    def test_backup_leaf_renewal_with_same_ca(
        self,
        ops_manager: MongoDBOpsManager,
        backed_up_mongodb: MongoDB,
        project_name: str,
        namespace: str,
        names: BackupCertificateNames,
    ):
        """Renewing the leaf on the same CA refreshes queryable.pem and keeps the query trusted."""
        revision, leaf = leaf_issuance(namespace, names.leaf_certificate)
        pem_before = queryable_pem(namespace, names)
        bundle_before = ca_bundle_snapshot(namespace, names.ca_bundle_config_map, CA_BUNDLE_KEY)
        renewal_before = wait_for_renewal_status_matching_certificate(ops_manager, "backup", names.leaf_certificate)
        ca = read_secret(namespace, names.ca_certificate)["tls.crt"]

        trigger_certificate_renewal(namespace, names.leaf_certificate)
        _, renewed_leaf = wait_for_leaf_reissued(namespace, names.leaf_certificate, revision, leaf)
        assert cert_signed_by_bundle(renewed_leaf, ca), "the renewed leaf must be signed by the same backup CA"

        renewed_pem = wait_for_queryable_pem(namespace, names, previous_pem=pem_before)
        assert renewed_leaf in renewed_pem, "queryable.pem must hold the renewed leaf"
        assert leaf not in renewed_pem, "queryable.pem must not hold the previous leaf"

        bundle_after = ca_bundle_snapshot(namespace, names.ca_bundle_config_map, CA_BUNDLE_KEY)
        assert bundle_after == bundle_before, "a leaf renewal on the same CA must not republish the backup bundle"
        assert read_secret(namespace, names.ca_certificate)["tls.crt"] == ca, "the backup CA must not change"

        wait_for_renewal_status_matching_certificate(
            ops_manager, "backup", names.leaf_certificate, newer_than=renewal_before["notAfter"]
        )
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

        _query(ops_manager, project_name, backed_up_mongodb, backup_ca_bundle(namespace, names))

    def test_backup_ca_rotation_widens_bundle(
        self,
        ops_manager: MongoDBOpsManager,
        backed_up_mongodb: MongoDB,
        project_name: str,
        namespace: str,
        names: BackupCertificateNames,
        rotation: dict,
    ):
        """Rotating the backup CA retains the previous CA until the leaf moves to the new one."""
        rotation["other_bundles"] = other_component_bundles(namespace, ops_manager)
        rotation["revision"], rotation["leaf"] = leaf_issuance(namespace, names.leaf_certificate)
        rotation["pem"] = queryable_pem(namespace, names)
        bundle = ca_bundle_snapshot(namespace, names.ca_bundle_config_map, CA_BUNDLE_KEY)

        rotation["old_ca"] = begin_ca_rotation(namespace, names.ca_certificate)
        rotation["new_ca"] = wait_for_ca_rotated(namespace, names.ca_certificate, rotation["old_ca"])

        widened = wait_for_bundle_rotation(
            namespace,
            names.ca_bundle_config_map,
            CA_BUNDLE_KEY,
            bundle["latest_hash"],
            bundle["published_at"],
        )
        rotation["widened"] = widened
        old_fingerprints = set(bundle_fingerprints(rotation["old_ca"]))
        new_fingerprints = set(bundle_fingerprints(rotation["new_ca"]))
        assert len(widened["certificates"]) == 2, "the widened backup bundle must hold exactly 2 CAs"
        assert (
            set(widened["certificates"]) == old_fingerprints | new_fingerprints
        ), "the widened backup bundle must hold both the old and the new CA"
        assert widened["previous_hash"] == bundle["latest_hash"], "previousHash must name the pre-rotation bundle"

        widened_pem = wait_for_queryable_pem(namespace, names, previous_pem=rotation["pem"])
        pem_fingerprints = set(bundle_fingerprints(widened_pem))
        assert old_fingerprints <= pem_fingerprints, "queryable.pem must still hold the previous CA"
        assert new_fingerprints <= pem_fingerprints, "queryable.pem must hold the new CA"
        assert rotation["leaf"] in widened_pem, "the leaf must not be re-issued before the bundle propagated"
        rotation["pem"] = widened_pem

        assert (
            other_component_bundles(namespace, ops_manager) == rotation["other_bundles"]
        ), "rotating the backup CA must not change the Ops Manager or AppDB bundles"

        wait_for_component_certificates_ready_message(ops_manager, "Backup", PREVIOUS_SERVER_CA_RETAINED)

        # The old leaf is still served, and the widened bundle still trusts it.
        _query(ops_manager, project_name, backed_up_mongodb, backup_ca_bundle(namespace, names))

    def test_backup_leaf_reissued_on_new_ca(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        names: BackupCertificateNames,
        rotation: dict,
    ):
        """The operator re-issues the leaf on the new CA once the widened bundle propagated."""
        revision, leaf = wait_for_leaf_reissued(
            namespace,
            names.leaf_certificate,
            rotation["revision"],
            rotation["leaf"],
            signed_by=rotation["new_ca"],
            timeout=1800,
        )
        assert revision > rotation["revision"]
        assert not cert_signed_by_bundle(leaf, rotation["old_ca"]), "the reissued leaf must not use the old CA"
        assert_issued_after_bundle_propagation(namespace, names.leaf_certificate, rotation["widened"]["published_at"])

        pem = wait_for_queryable_pem(namespace, names, previous_pem=rotation["pem"])
        assert leaf in pem, "queryable.pem must hold the leaf issued by the new CA"
        assert rotation["leaf"] not in pem, "queryable.pem must not hold the leaf of the old CA"
        assert set(bundle_fingerprints(rotation["old_ca"])) <= set(
            bundle_fingerprints(pem)
        ), "queryable.pem must keep trusting the previous CA until it is pruned"
        rotation["leaf"] = leaf
        rotation["pem"] = pem

        snapshot = ca_bundle_snapshot(namespace, names.ca_bundle_config_map, CA_BUNDLE_KEY)
        assert snapshot["latest_hash"] == rotation["widened"]["latest_hash"], "re-issuing must not change the bundle"

        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        wait_for_component_certificates_ready_message(ops_manager, "Backup", PREVIOUS_SERVER_CA_RETAINED)

    def test_query_trusts_only_the_new_ca(
        self,
        ops_manager: MongoDBOpsManager,
        backed_up_mongodb: MongoDB,
        project_name: str,
        rotation: dict,
    ):
        """The query endpoint serves the new leaf: the new CA alone trusts it and the old CA does not."""
        _query(ops_manager, project_name, backed_up_mongodb, rotation["new_ca"])
        _assert_query_rejected(ops_manager, project_name, backed_up_mongodb, rotation["old_ca"])
        # The CA Ops Manager hands out for snapshot queries trusts the new leaf as well.
        _query(ops_manager, project_name, backed_up_mongodb, None)

    def test_prune_backup_previous_ca(
        self,
        ops_manager: MongoDBOpsManager,
        backed_up_mongodb: MongoDB,
        project_name: str,
        namespace: str,
        names: BackupCertificateNames,
        rotation: dict,
    ):
        """Pruning the backup CA drops the old root from the bundle and from queryable.pem."""
        request_prune_previous_ca(ops_manager, "backup")
        wait_for_prune_annotation_removed(ops_manager)

        pruned = wait_for_bundle_pruned(namespace, names.ca_bundle_config_map, CA_BUNDLE_KEY, rotation["new_ca"])
        assert pruned["certificates"] == bundle_fingerprints(
            rotation["new_ca"]
        ), "the pruned backup bundle must hold only the new CA"

        pem = wait_for_queryable_pem(namespace, names, previous_pem=rotation["pem"])
        assert not set(bundle_fingerprints(rotation["old_ca"])) & set(
            bundle_fingerprints(pem)
        ), "queryable.pem must not hold the pruned CA"
        assert rotation["leaf"] in pem, "pruning must not re-issue the leaf"

        assert (
            other_component_bundles(namespace, ops_manager) == rotation["other_bundles"]
        ), "pruning the backup CA must not change the Ops Manager or AppDB bundles"
        wait_for_component_certificates_ready_message(
            ops_manager, "Backup", PREVIOUS_SERVER_CA_RETAINED, present=False, timeout=900
        )
        wait_for_certificates_ready(ops_manager)

        _query(ops_manager, project_name, backed_up_mongodb, backup_ca_bundle(namespace, names))
        _assert_query_rejected(ops_manager, project_name, backed_up_mongodb, rotation["old_ca"])
