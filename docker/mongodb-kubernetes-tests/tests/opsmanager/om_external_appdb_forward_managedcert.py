from pathlib import Path

import pytest
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.opsmanager.managedcert_helper import cert_manager, default_operator
from tests.opsmanager.om_external_appdb_test_helpers import (
    ManagedAppDBScenario,
    appdb_cluster_domain,
    appdb_role_resource,
    assert_managed_appdb_chain,
    assert_managed_appdb_graph_owner,
    assert_no_migration_annotations,
    configure_appdb_role_mongodb,
    issuer,
    managed_appdb_graph,
    managed_appdb_security,
    meta_om,
    wait_for_managed_appdb_graph_owner,
    wait_for_managed_appdb_measurements,
    without_managed_certificate_owner_uid,
)


def build_scenario(namespace, version, appdb_version, meta, name, issuer_name=None) -> ManagedAppDBScenario:
    primary = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary_om.yaml"), name=name, namespace=namespace
    )
    primary.set_version(version)
    primary.set_appdb_version(appdb_version)
    security = managed_appdb_security(issuer_name)
    primary["spec"]["applicationDatabase"]["security"] = security
    database = appdb_role_resource(namespace, ensure_ent_version(appdb_version), name + "-db")
    database["spec"]["security"] = security
    database["spec"]["clusterDomain"] = appdb_cluster_domain(primary["spec"])
    configure_appdb_role_mongodb(database, meta, namespace)
    return ManagedAppDBScenario(primary, database, meta)


class ForwardManagedAppDBTests:
    def test_deploy_internal_managed_appdb(self, scenario: ManagedAppDBScenario):
        scenario.primary.create()
        scenario.primary.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        scenario.primary.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        assert_managed_appdb_chain(scenario)

    def test_renew_source_leaf_before_handover(self, scenario: ManagedAppDBScenario, tmp_path: Path):
        leaf = scenario.renew_leaf(require_previous_hash=True)
        scenario.wait_for_served_leaf(tmp_path, leaf)
        scenario.primary.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_capture_trust_credentials_and_primary_pods(self, scenario: ManagedAppDBScenario, tmp_path: Path):
        scenario.primary.load()
        scenario.graph = managed_appdb_graph(scenario.namespace, scenario.database.name)
        assert_managed_appdb_graph_owner(scenario.graph, scenario.primary)
        scenario.primary_pod_uids = scenario.current_primary_pod_uids()
        assert scenario.primary_pod_uids
        scenario.uri = scenario.wait_for_served_leaf(tmp_path)
        scenario.write_sentinel(tmp_path)

    def test_external_pending_without_reference(self, scenario: ManagedAppDBScenario):
        scenario.database.create()
        scenario.database.assert_reaches_phase(
            Phase.Pending, msg_regexp="Cannot take ownership of the AppDB Statefulset", timeout=300
        )
        assert managed_appdb_graph(scenario.namespace, scenario.database.name) == scenario.graph

    def test_set_external_ref_and_wait_for_handover(self, scenario: ManagedAppDBScenario):
        scenario.primary.load()
        scenario.primary["spec"]["externalApplicationDatabaseRef"] = {"name": scenario.database.name, "kind": "MongoDB"}
        scenario.primary.update()
        scenario.database.assert_reaches_phase(Phase.Running, timeout=1800)
        scenario.primary.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        scenario.primary.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)

    def test_handover_preserves_graph_credentials_and_data(self, scenario: ManagedAppDBScenario, tmp_path: Path):
        scenario.database.load()
        # The Certificate specs are rewritten with the database's owner markers synchronously; wait for
        # cert-manager to copy them onto the issued Secrets before snapshotting the graph.
        wait_for_managed_appdb_graph_owner(scenario.namespace, scenario.database.name, scenario.database)
        graph = managed_appdb_graph(scenario.namespace, scenario.database.name)
        assert set(graph) == set(scenario.graph)
        for key, before in scenario.graph.items():
            assert graph[key]["uid"] == before["uid"], key
            # The owner UID label is an ownership marker, so it is the one part of the spec that has
            # to change: assert_managed_appdb_graph_owner below pins it to the database's UID.
            assert without_managed_certificate_owner_uid(graph[key]["spec"]) == without_managed_certificate_owner_uid(
                before["spec"]
            ), key
            assert graph[key]["data"] == before["data"], key
        assert_managed_appdb_graph_owner(graph, scenario.database)
        assert scenario.primary.read_appdb_connection_url() == scenario.uri
        scenario.assert_sentinel_present(tmp_path)

    def test_primary_om_did_not_roll(self, scenario: ManagedAppDBScenario):
        assert scenario.current_primary_pod_uids() == scenario.primary_pod_uids

    def test_verified_tls_scram_and_monitoring(self, scenario: ManagedAppDBScenario, tmp_path: Path):
        scenario.wait_for_served_leaf(tmp_path)
        scenario.assert_sentinel_present(tmp_path)
        wait_for_managed_appdb_measurements(scenario)

    def test_handover_markers_cleared(self, scenario: ManagedAppDBScenario):
        assert_no_migration_annotations(scenario.namespace, scenario.database.name)

    def test_renewal_after_handover(self, scenario: ManagedAppDBScenario, tmp_path: Path):
        leaf = scenario.renew_leaf()
        assert_managed_appdb_chain(scenario)
        scenario.database.load()
        graph = managed_appdb_graph(scenario.namespace, scenario.database.name)
        assert_managed_appdb_graph_owner(graph, scenario.database)
        scenario.wait_for_served_leaf(tmp_path, leaf)
        scenario.assert_sentinel_present(tmp_path)
        assert scenario.current_primary_pod_uids() == scenario.primary_pod_uids


@pytest.mark.e2e_om_external_appdb_forward_managedcert
class TestManagedAppDBForwardSelfSigned(ForwardManagedAppDBTests):
    @pytest.fixture(scope="class")
    def scenario(self, namespace, custom_version, custom_appdb_version, meta_om):
        return build_scenario(namespace, custom_version, custom_appdb_version, meta_om, "forward-managed-selfsigned")


@pytest.mark.e2e_om_external_appdb_forward_managedcert
class TestManagedAppDBForwardCustomerIssuer(ForwardManagedAppDBTests):
    @pytest.fixture(scope="class")
    def scenario(self, namespace, custom_version, custom_appdb_version, meta_om, issuer):
        return build_scenario(
            namespace, custom_version, custom_appdb_version, meta_om, "forward-managed-customer", issuer
        )
