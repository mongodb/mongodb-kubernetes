from pathlib import Path

import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongotester import connection_string_without_query_param
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo.errors import ServerSelectionTimeoutError
from tests.opsmanager.managedcert_helper import (
    OM_APP_DB_CA_DIR,
    OM_APP_DB_CA_ENV_VAR,
    OM_APP_DB_CA_FILE_PATH,
    OM_CONTAINER,
    _assert_env_value,
    _assert_volume_mount,
    cert_manager,
    default_operator,
)
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
    wait_for_managed_appdb_measurements,
)


def build_scenario(namespace, version, mdb_version, meta, name, issuer_name=None) -> ManagedAppDBScenario:
    primary = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary_om_no_appdb.yaml"), name=name, namespace=namespace
    )
    primary.set_version(version)
    primary["spec"]["externalApplicationDatabaseRef"] = {"name": name + "-db", "kind": "MongoDB"}
    database = appdb_role_resource(namespace, mdb_version, name + "-db")
    database["spec"]["security"] = managed_appdb_security(issuer_name)
    configure_appdb_role_mongodb(database, meta, namespace)
    return ManagedAppDBScenario(primary, database, meta)


class FreshManagedAppDBTests:
    def test_create_external_appdb_with_managed_certificates(self, scenario: ManagedAppDBScenario):
        assert not try_load(scenario.primary), "primary OM must not exist before the external database"
        scenario.database.create()
        scenario.database.assert_reaches_phase(Phase.Running, timeout=1800)

    def test_graph_owned_before_primary_om(self, scenario: ManagedAppDBScenario):
        assert_managed_appdb_chain(scenario)
        scenario.database.load()
        scenario.graph = managed_appdb_graph(scenario.namespace, scenario.database.name)
        assert f"Certificate/{scenario.leaf_name}" in scenario.graph
        assert f"ConfigMap/{scenario.ca_name}" in scenario.graph
        assert_managed_appdb_graph_owner(scenario.graph, scenario.database)
        assert_no_migration_annotations(scenario.namespace, scenario.database.name)

    def test_create_primary_om_with_external_reference(self, scenario: ManagedAppDBScenario):
        scenario.primary.create()
        scenario.primary.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        scenario.primary.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)
        scenario.primary.load()
        assert "applicationDatabase" not in scenario.primary["spec"]

    def test_primary_om_uses_external_managed_ca(self, scenario: ManagedAppDBScenario):
        sts = scenario.primary.read_statefulset()
        volume = next(v for v in sts.spec.template.spec.volumes if v.name == "appdb-ca-certificate")
        assert volume.config_map.name == scenario.ca_name
        _assert_volume_mount(sts, OM_CONTAINER, volume.name, OM_APP_DB_CA_DIR)
        _assert_env_value(sts, OM_CONTAINER, OM_APP_DB_CA_ENV_VAR, OM_APP_DB_CA_FILE_PATH)
        parsed = pymongo.uri_parser.parse_uri(scenario.primary.read_appdb_connection_url())
        domain = appdb_cluster_domain(scenario.database["spec"])
        assert set(parsed["nodelist"]) == {
            (f"{scenario.database.name}-{i}.{scenario.database.name}-svc.{scenario.namespace}.svc.{domain}", 27017)
            for i in range(scenario.database["spec"]["members"])
        }
        assert parsed["options"].get("tls") is True

    def test_verified_tls_scram_connectivity(self, scenario: ManagedAppDBScenario, tmp_path: Path):
        scenario.wait_for_served_leaf(tmp_path)
        scenario.write_sentinel(tmp_path)
        scenario.assert_sentinel_present(tmp_path)

    def test_reject_plaintext_and_wrong_ca(self, scenario: ManagedAppDBScenario, issuer_ca_filepath: str):
        uri = scenario.primary.read_appdb_connection_url()
        plaintext_uri = connection_string_without_query_param(connection_string_without_query_param(uri, "ssl"), "tls")
        with pymongo.MongoClient(
            plaintext_uri, tls=False, serverSelectionTimeoutMS=5000, connectTimeoutMS=5000
        ) as plain:
            with pytest.raises(ServerSelectionTimeoutError):
                plain.admin.command("hello")
        with pymongo.MongoClient(
            uri, tlsCAFile=issuer_ca_filepath, serverSelectionTimeoutMS=5000, connectTimeoutMS=5000
        ) as wrong:
            with pytest.raises(ServerSelectionTimeoutError):
                wrong.admin.command("hello")

    def test_monitoring_and_goal_state(self, scenario: ManagedAppDBScenario):
        scenario.database.assert_reaches_phase(Phase.Running, timeout=600)
        wait_for_managed_appdb_measurements(scenario)

    def test_no_migration_or_om_owned_graph(self, scenario: ManagedAppDBScenario):
        graph = managed_appdb_graph(scenario.namespace, scenario.database.name)
        assert graph == scenario.graph
        assert_no_migration_annotations(scenario.namespace, scenario.database.name)

    def test_renewal_preserves_external_ownership_and_connectivity(
        self, scenario: ManagedAppDBScenario, tmp_path: Path
    ):
        leaf = scenario.renew_leaf()
        assert_managed_appdb_chain(scenario)
        scenario.wait_for_served_leaf(tmp_path, leaf)
        after = managed_appdb_graph(scenario.namespace, scenario.database.name)
        assert set(after) == set(scenario.graph)
        for key, original in scenario.graph.items():
            assert after[key]["uid"] == original["uid"]
            assert after[key]["owners"] == original["owners"]
            assert after[key]["spec"] == original["spec"]
            if scenario.leaf_name not in key:
                assert after[key]["data"] == original["data"]
        scenario.assert_sentinel_present(tmp_path)


@pytest.mark.e2e_om_external_appdb_fresh_managedcert
class TestFreshExternalAppDBManagedSelfSigned(FreshManagedAppDBTests):
    @pytest.fixture(scope="class")
    def scenario(self, namespace, custom_version, custom_mdb_version, meta_om):
        return build_scenario(namespace, custom_version, custom_mdb_version, meta_om, "fresh-managed-selfsigned")


@pytest.mark.e2e_om_external_appdb_fresh_managedcert
class TestFreshExternalAppDBManagedCustomerIssuer(FreshManagedAppDBTests):
    @pytest.fixture(scope="class")
    def scenario(self, namespace, custom_version, custom_mdb_version, meta_om, issuer):
        return build_scenario(namespace, custom_version, custom_mdb_version, meta_om, "fresh-managed-customer", issuer)
