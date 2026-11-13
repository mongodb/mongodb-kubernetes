"""Edge cases of the safe self-signed CA rotation for Ops Manager managed certificates.

The scenario deploys an Ops Manager with a self-signed operator-managed HTTPS certificate, a
self-signed managed AppDB, and a small replica set managed by this Ops Manager whose project
trusts the published Ops Manager CA bundle (sslMMSCAConfigMap). It then walks through:

1. Hot reload: an AppDB CA rotation does not restart Ops Manager, and an Ops Manager CA rotation
   does not recreate the AppDB pods. Both read the other side's CA from a mounted ConfigMap.
2. Downstream MongoDB: the replica set's agents keep reaching Ops Manager during and after the
   Ops Manager CA rotation and after the previous CA is pruned, and a spec change still reconciles.
3. An operator restart during the propagation window keeps the published-at clock, and the leaf
   is not re-issued before the propagation delay has passed.
4. cert-manager renewing the Ops Manager leaf on its own during the window converges.
5. Prune annotation values that name no Ops Manager component are kept and prune nothing, and
   requests with nothing to prune, or for a disabled component, are removed promptly.
6. Editing a ConfigMap referenced by additionalTrustedCAs republishes the AppDB bundle without
   recreating the AppDB pods.
"""

from pathlib import Path

import pytest
from kubetester import create_or_update_configmap, read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import local_operator
from tests.opsmanager.managedcert_helper import (
    APP_DB_CA_BUNDLE_KEY,
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
    request_prune_previous_ca,
    wait_for_appdb_ca_file_path,
    wait_for_bundle_pruned,
    wait_for_bundle_rotation,
    wait_for_ca_rotated,
    wait_for_certificates_ready,
    wait_for_component_certificates_ready_message,
    wait_for_leaf_reissued_on_new_ca,
    wait_for_om_https_trusted_by_bundle,
    wait_for_prune_annotation_removed,
)
from tests.opsmanager.managedcert_rotation_edge_helper import (
    CLOCK_SKEW,
    MANAGED_CA_PROPAGATION_DELAY,
    appdb_pod_states,
    assert_pods_not_recreated,
    assert_prune_annotation_kept,
    certificate_not_before,
    delete_operator_pods,
    generate_ca_pem,
    managed_om_replica_set,
    now_utc,
    om_pod_states,
    parse_time,
    wait_for_appdb_agents_reach_om,
    wait_for_appdb_mounted_om_bundle,
    wait_for_bundle_trusting,
    wait_for_database_agents_reach_om,
    wait_for_database_mounted_om_bundle,
    wait_for_gated_leaf_reissue,
    wait_for_mongodb_running_at_current_generation,
    wait_for_om_mounted_appdb_bundle,
    wait_for_om_serving_leaf_signed_by,
)
from tests.tls.managedcert_helper import pem_secret_hashes, read_certificate_status, trigger_certificate_renewal

OM_NAME = "om-managed-cert-edge"
REPLICA_SET_NAME = "managed-om-ca-rs"
EXTRA_CA_CONFIG_MAP = f"{OM_NAME}-extra-appdb-ca"
EXTRA_CA_KEY = "ca.crt"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    return managed_ops_manager(OM_NAME, namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    """Built on first use, once Ops Manager is Running and reports its HTTPS URL."""
    return managed_om_replica_set(ops_manager, REPLICA_SET_NAME, namespace, custom_mdb_version)


@fixture(scope="module")
def rotation() -> dict:
    """The CA, leaf and pod state each step hands to the next one."""
    return {}


def _leaf_baseline(namespace: str, certificate_name: str, pem_secret_name: str) -> dict:
    return {
        "leaf": read_secret(namespace, certificate_name)["tls.crt"],
        "revision": read_certificate_status(namespace, certificate_name).get("revision", 0),
        "pem_hash": pem_secret_hashes(namespace, pem_secret_name)[0],
    }


def _assert_bundle_holds(snapshot: dict, *ca_pems: str):
    expected = {fingerprint for pem in ca_pems for fingerprint in bundle_fingerprints(pem)}
    assert set(snapshot["certificates"]) == expected, f"the bundle must hold exactly the expected CAs: {snapshot}"


@pytest.mark.e2e_om_ops_manager_managedcert_rotation_edge_cases
class TestOpsManagerManagedCertificateRotationEdgeCases:
    def test_install_operator(self, default_operator: Operator):
        default_operator.wait_for_operator_ready()

    def test_ops_manager_and_appdb_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        wait_for_certificates_ready(ops_manager)

        om_url = ops_manager.om_status().get_url()
        assert om_url is not None and om_url.startswith("https://")

    def test_downstream_replica_set_trusts_managed_om_bundle(
        self, ops_manager: MongoDBOpsManager, replica_set: MongoDB, namespace: str
    ):
        """The replica set reaches Running against the HTTPS Ops Manager with the managed CA bundle."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=1200)
        replica_set.get_om_tester().wait_agents_ready(timeout=900)

        om_bundle = latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        wait_for_database_mounted_om_bundle(replica_set, om_bundle)

    # --- 1. Hot reload: AppDB CA rotation ------------------------------------------

    def test_appdb_ca_rotation_reaches_ops_manager_without_restart(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        """The widened AppDB bundle reaches the running Ops Manager pods through the kubelet sync."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        rotation["om_pods"] = om_pod_states(ops_manager)
        rotation["appdb_pods"] = appdb_pod_states(ops_manager)
        assert rotation["om_pods"], "Ops Manager pods must exist before the AppDB CA rotation"
        assert rotation["appdb_pods"], "AppDB pods must exist before the AppDB CA rotation"

        rotation["appdb"] = _leaf_baseline(namespace, names.app_db_leaf_certificate, names.app_db_pem_secret)
        bundle = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)

        old_ca = begin_ca_rotation(namespace, names.app_db_ca_certificate)
        new_ca = wait_for_ca_rotated(namespace, names.app_db_ca_certificate, old_ca)
        rotation["appdb_old_ca"], rotation["appdb_new_ca"] = old_ca, new_ca

        widened = wait_for_bundle_rotation(
            namespace,
            names.app_db_ca_bundle_config_map,
            APP_DB_CA_BUNDLE_KEY,
            bundle["latest_hash"],
            bundle["published_at"],
        )
        _assert_bundle_holds(widened, old_ca, new_ca)

        widened_pem = latest_ca_bundle_pem(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        wait_for_om_mounted_appdb_bundle(ops_manager, widened_pem)
        assert_pods_not_recreated(rotation["om_pods"], om_pod_states(ops_manager), "Ops Manager", check_restarts=True)

    def test_appdb_leaf_reissued_without_restarting_ops_manager(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        """After the AppDB leaf moves to the new root, Ops Manager keeps using the AppDB unrestarted."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        leaf, pem_hash = wait_for_leaf_reissued_on_new_ca(
            namespace,
            names.app_db_leaf_certificate,
            names.app_db_pem_secret,
            rotation["appdb"]["leaf"],
            rotation["appdb"]["pem_hash"],
            rotation["appdb_new_ca"],
            timeout=1800,
        )
        assert not cert_signed_by_bundle(leaf, rotation["appdb_old_ca"])

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)

        since = now_utc()
        ops_manager.get_om_tester().assert_healthiness()
        ops_manager.get_om_tester(ops_manager.app_db_name()).assert_group_exists()
        # Ops Manager stores the agent check-ins in the AppDB, so fresh check-ins prove that it
        # still reads and writes the AppDB over TLS with the re-issued leaf.
        wait_for_appdb_agents_reach_om(ops_manager, since)

        assert_pods_not_recreated(rotation["om_pods"], om_pod_states(ops_manager), "Ops Manager", check_restarts=True)
        assert_pods_not_recreated(rotation["appdb_pods"], appdb_pod_states(ops_manager), "AppDB")

    def test_prune_appdb_previous_ca_without_restarting_ops_manager(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        request_prune_previous_ca(ops_manager, "appdb")
        wait_for_prune_annotation_removed(ops_manager)
        wait_for_bundle_pruned(
            namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY, rotation["appdb_new_ca"]
        )

        pruned_pem = latest_ca_bundle_pem(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        wait_for_om_mounted_appdb_bundle(ops_manager, pruned_pem)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.get_om_tester().assert_healthiness()
        assert_pods_not_recreated(rotation["om_pods"], om_pod_states(ops_manager), "Ops Manager", check_restarts=True)

    # --- 1. Hot reload and 2. downstream MongoDB: Ops Manager CA rotation ------------

    def test_om_ca_rotation_reaches_appdb_and_downstream_agents(
        self, ops_manager: MongoDBOpsManager, replica_set: MongoDB, namespace: str, rotation: dict
    ):
        """During the window the widened bundle reaches every agent and Ops Manager stays reachable."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        replica_set.load()
        rotation["appdb_pods"] = appdb_pod_states(ops_manager)
        rotation["om"] = _leaf_baseline(namespace, names.om_leaf_certificate, names.om_pem_secret)
        bundle = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)

        old_ca = begin_ca_rotation(namespace, names.om_ca_certificate)
        new_ca = wait_for_ca_rotated(namespace, names.om_ca_certificate, old_ca)
        rotation["om_old_ca"], rotation["om_new_ca"] = old_ca, new_ca

        widened = wait_for_bundle_rotation(
            namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY, bundle["latest_hash"], bundle["published_at"]
        )
        _assert_bundle_holds(widened, old_ca, new_ca)

        widened_pem = latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        wait_for_appdb_mounted_om_bundle(ops_manager, widened_pem)
        wait_for_database_mounted_om_bundle(replica_set, widened_pem)
        wait_for_om_https_trusted_by_bundle(ops_manager, widened_pem, timeout=900)

        since = now_utc()
        wait_for_appdb_agents_reach_om(ops_manager, since)
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)
        wait_for_database_agents_reach_om(replica_set, since)
        assert_pods_not_recreated(rotation["appdb_pods"], appdb_pod_states(ops_manager), "AppDB")

    def test_om_leaf_reissued_without_recreating_appdb_pods(
        self, ops_manager: MongoDBOpsManager, replica_set: MongoDB, namespace: str, rotation: dict
    ):
        """Once Ops Manager serves the leaf on the new root, the agents still reach it unrestarted."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        wait_for_leaf_reissued_on_new_ca(
            namespace,
            names.om_leaf_certificate,
            names.om_pem_secret,
            rotation["om"]["leaf"],
            rotation["om"]["pem_hash"],
            rotation["om_new_ca"],
            timeout=1800,
        )
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        wait_for_om_serving_leaf_signed_by(ops_manager, rotation["om_new_ca"], rotation["om_old_ca"])

        since = now_utc()
        wait_for_appdb_agents_reach_om(ops_manager, since)
        ops_manager.assert_appdb_monitoring_group_was_created()
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)
        wait_for_database_agents_reach_om(replica_set, since)

        assert_pods_not_recreated(rotation["appdb_pods"], appdb_pod_states(ops_manager), "AppDB")

    def test_prune_om_previous_ca_keeps_downstream_connected(
        self, ops_manager: MongoDBOpsManager, replica_set: MongoDB, namespace: str, rotation: dict
    ):
        """With only the new root left in the bundle, every agent still reaches Ops Manager."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        request_prune_previous_ca(ops_manager, "om")
        wait_for_prune_annotation_removed(ops_manager)
        wait_for_bundle_pruned(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY, rotation["om_new_ca"])

        pruned_pem = latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        wait_for_om_https_trusted_by_bundle(ops_manager, pruned_pem, timeout=900)
        wait_for_appdb_mounted_om_bundle(ops_manager, pruned_pem)
        wait_for_database_mounted_om_bundle(replica_set, pruned_pem)

        since = now_utc()
        wait_for_appdb_agents_reach_om(ops_manager, since)
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)
        wait_for_database_agents_reach_om(replica_set, since)
        assert_pods_not_recreated(rotation["appdb_pods"], appdb_pod_states(ops_manager), "AppDB")

    def test_downstream_spec_change_reconciles_after_prune(self, replica_set: MongoDB):
        """The operator still talks to Ops Manager with the pruned bundle for the downstream project."""
        replica_set.load()
        replica_set["spec"]["logLevel"] = "INFO" if replica_set["spec"].get("logLevel") == "DEBUG" else "DEBUG"
        replica_set.update()

        wait_for_mongodb_running_at_current_generation(replica_set, timeout=1200)
        wait_for_database_agents_reach_om(replica_set, now_utc())

    # --- 3. Operator restart in the propagation window -------------------------------

    def test_operator_restart_after_appdb_bundle_published(
        self, default_operator: Operator, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        """The operator restarts right after it published the widened AppDB bundle."""
        if local_operator():
            pytest.skip("the operator runs outside the cluster, there is no operator pod to restart")

        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        rotation["appdb"] = _leaf_baseline(namespace, names.app_db_leaf_certificate, names.app_db_pem_secret)
        bundle = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)

        old_ca = begin_ca_rotation(namespace, names.app_db_ca_certificate)
        new_ca = wait_for_ca_rotated(namespace, names.app_db_ca_certificate, old_ca)
        rotation["appdb_old_ca"], rotation["appdb_new_ca"] = old_ca, new_ca

        widened = wait_for_bundle_rotation(
            namespace,
            names.app_db_ca_bundle_config_map,
            APP_DB_CA_BUNDLE_KEY,
            bundle["latest_hash"],
            bundle["published_at"],
        )
        rotation["appdb_widened"] = widened
        # The leaf must still be on the old root: the propagation delay has only just started.
        assert read_secret(namespace, names.app_db_leaf_certificate)["tls.crt"] == rotation["appdb"]["leaf"]

        delete_operator_pods(default_operator)
        default_operator.wait_for_operator_ready()

    def test_appdb_leaf_reissued_after_operator_restart_honours_delay(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        """The restarted operator re-issues the leaf, and not before the delay after published-at."""
        if "appdb_widened" not in rotation:
            pytest.skip("the operator was not restarted")

        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        widened = rotation["appdb_widened"]
        gate = parse_time(widened["published_at"]) + MANAGED_CA_PROPAGATION_DELAY - CLOCK_SKEW

        reissue = wait_for_gated_leaf_reissue(
            namespace,
            names.app_db_leaf_certificate,
            rotation["appdb"]["leaf"],
            rotation["appdb"]["revision"],
            rotation["appdb_new_ca"],
            timeout=1800,
        )
        not_before = certificate_not_before(reissue["leaf"])
        assert not_before >= gate, (
            f"the AppDB leaf was re-issued at {not_before.isoformat()}, earlier than the propagation delay "
            f"after the bundle was published at {widened['published_at']}"
        )
        issuing = reissue["rotated_issuing"]
        if issuing and issuing.get("lastTransitionTime"):
            assert parse_time(issuing["lastTransitionTime"]) >= gate, f"re-issue requested too early: {issuing}"
        assert (
            reissue["revision"] == rotation["appdb"]["revision"] + 1
        ), "the AppDB leaf must be re-issued exactly once after the CA rotation"

        snapshot = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        assert (
            snapshot["published_at"] == widened["published_at"]
        ), "the published-at annotation must survive the operator restart"
        assert snapshot["latest_hash"] == widened["latest_hash"], "the restart must not republish the bundle"

        wait_for_leaf_reissued_on_new_ca(
            namespace,
            names.app_db_leaf_certificate,
            names.app_db_pem_secret,
            rotation["appdb"]["leaf"],
            rotation["appdb"]["pem_hash"],
            rotation["appdb_new_ca"],
            timeout=900,
        )
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)

        request_prune_previous_ca(ops_manager, "appdb")
        wait_for_prune_annotation_removed(ops_manager)
        wait_for_bundle_pruned(
            namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY, rotation["appdb_new_ca"]
        )

    # --- 4. cert-manager renews the leaf during the window ---------------------------

    def test_cert_manager_renews_om_leaf_during_window(
        self, ops_manager: MongoDBOpsManager, replica_set: MongoDB, namespace: str, rotation: dict
    ):
        """A renewal that cert-manager starts on its own before the delay still converges."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        rotation["om"] = _leaf_baseline(namespace, names.om_leaf_certificate, names.om_pem_secret)
        bundle = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)

        old_ca = begin_ca_rotation(namespace, names.om_ca_certificate)
        new_ca = wait_for_ca_rotated(namespace, names.om_ca_certificate, old_ca)
        rotation["om_old_ca"], rotation["om_new_ca"] = old_ca, new_ca
        trigger_certificate_renewal(namespace, names.om_leaf_certificate)

        widened = wait_for_bundle_rotation(
            namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY, bundle["latest_hash"], bundle["published_at"]
        )
        _assert_bundle_holds(widened, old_ca, new_ca)

        wait_for_leaf_reissued_on_new_ca(
            namespace,
            names.om_leaf_certificate,
            names.om_pem_secret,
            rotation["om"]["leaf"],
            rotation["om"]["pem_hash"],
            new_ca,
            timeout=1800,
        )
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)

        widened_pem = latest_ca_bundle_pem(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        wait_for_om_https_trusted_by_bundle(ops_manager, widened_pem, timeout=1800)
        wait_for_om_serving_leaf_signed_by(ops_manager, new_ca, old_ca)
        wait_for_certificates_ready(ops_manager)
        wait_for_component_certificates_ready_message(ops_manager, "OpsManager", PREVIOUS_SERVER_CA_RETAINED)

        snapshot = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        _assert_bundle_holds(snapshot, old_ca, new_ca)

        since = now_utc()
        wait_for_appdb_agents_reach_om(ops_manager, since)
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)
        wait_for_database_agents_reach_om(replica_set, since)

    # --- 5. Prune annotation values --------------------------------------------------

    @pytest.mark.parametrize("value", ["server", "foo"])
    def test_prune_value_without_om_component_is_kept(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict, value: str
    ):
        """A value naming no Ops Manager component stays on the resource and prunes nothing."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        om_bundle = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        appdb_bundle = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        _assert_bundle_holds(om_bundle, rotation["om_old_ca"], rotation["om_new_ca"])

        def bundles_unchanged():
            assert (
                ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY) == om_bundle
            ), "the Ops Manager bundle must keep the retained CA"
            assert (
                ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY) == appdb_bundle
            ), "the AppDB bundle must not change"

        request_prune_previous_ca(ops_manager, value)
        assert_prune_annotation_kept(ops_manager, value, duration=150, check=bundles_unchanged)

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        wait_for_component_certificates_ready_message(ops_manager, "OpsManager", PREVIOUS_SERVER_CA_RETAINED)

    def test_prune_om_after_unknown_values(self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict):
        """Replacing the ignored value with om prunes the retained Ops Manager CA."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)

        request_prune_previous_ca(ops_manager, "om")
        wait_for_prune_annotation_removed(ops_manager)
        wait_for_bundle_pruned(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY, rotation["om_new_ca"])
        wait_for_component_certificates_ready_message(
            ops_manager, "OpsManager", PREVIOUS_SERVER_CA_RETAINED, present=False
        )

    @pytest.mark.parametrize("value", ["om", "backup"])
    def test_prune_with_nothing_to_prune_is_removed(self, ops_manager: MongoDBOpsManager, namespace: str, value: str):
        """om with nothing retained, or backup with backup certificates disabled, is acknowledged at once."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        om_bundle = ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        appdb_bundle = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        assert len(om_bundle["certificates"]) == 1 and len(appdb_bundle["certificates"]) == 1, "no CA may be retained"

        request_prune_previous_ca(ops_manager, value)
        wait_for_prune_annotation_removed(ops_manager, timeout=300)

        assert ca_bundle_snapshot(namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY) == om_bundle
        assert ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY) == appdb_bundle

    # --- 6. Watches on additionalTrustedCAs ------------------------------------------

    def test_appdb_additional_trusted_ca_is_published(
        self, ops_manager: MongoDBOpsManager, namespace: str, rotation: dict
    ):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        rotation["extra_ca"] = generate_ca_pem("managed-cert-edge-extra-ca-1")
        create_or_update_configmap(namespace, EXTRA_CA_CONFIG_MAP, {EXTRA_CA_KEY: rotation["extra_ca"]})
        bundle = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)

        ops_manager.load()
        app_db_security = ops_manager["spec"]["applicationDatabase"].setdefault("security", {})
        managed_certificate = app_db_security.setdefault("managedCertificate", {})
        managed_certificate.setdefault("server", {})["additionalTrustedCAs"] = [
            {"name": EXTRA_CA_CONFIG_MAP, "kind": "ConfigMap", "key": EXTRA_CA_KEY}
        ]
        ops_manager.update()

        managed_ca = read_secret(namespace, names.app_db_ca_certificate)["tls.crt"]
        published = wait_for_bundle_trusting(
            namespace,
            names.app_db_ca_bundle_config_map,
            APP_DB_CA_BUNDLE_KEY,
            bundle["latest_hash"],
            [managed_ca, rotation["extra_ca"]],
        )
        wait_for_appdb_ca_file_path(ops_manager, published["latest_hash"])
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        wait_for_certificates_ready(ops_manager)

    def test_editing_additional_trusted_ca_config_map_republishes_bundle(
        self, ops_manager: MongoDBOpsManager, namespace: str, tmp_path: Path
    ):
        """The operator watches the referenced ConfigMap and rolls the new CA in without recreating pods."""
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        appdb_pods = appdb_pod_states(ops_manager)
        bundle = ca_bundle_snapshot(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        managed_ca = read_secret(namespace, names.app_db_ca_certificate)["tls.crt"]

        replacement_ca = generate_ca_pem("managed-cert-edge-extra-ca-2")
        create_or_update_configmap(namespace, EXTRA_CA_CONFIG_MAP, {EXTRA_CA_KEY: replacement_ca})

        published = wait_for_bundle_trusting(
            namespace,
            names.app_db_ca_bundle_config_map,
            APP_DB_CA_BUNDLE_KEY,
            bundle["latest_hash"],
            [managed_ca, replacement_ca],
        )
        automation_config = wait_for_appdb_ca_file_path(ops_manager, published["latest_hash"])
        assert automation_config["tls"]["CAFilePath"].endswith(published["latest_hash"])

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        published_pem = latest_ca_bundle_pem(namespace, names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY)
        wait_for_om_mounted_appdb_bundle(ops_manager, published_pem)

        ca_file = tmp_path / "appdb-ca.pem"
        ca_file.write_text(published_pem)
        tester = ops_manager.get_appdb_tester(authenticate=True, ssl=True, ca_path=str(ca_file))
        try:
            tester.assert_connectivity()
        finally:
            tester.client.close()

        assert_pods_not_recreated(appdb_pods, appdb_pod_states(ops_manager), "AppDB")
