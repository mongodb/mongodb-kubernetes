from pathlib import Path
from typing import ClassVar, Optional

import kubernetes.client
import pytest
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_member_cluster_api_client,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    assert_no_appdb_migration_annotations,
    configure_appdb_role_mongodb_multi,
    read_appdb_connection_url,
)
from tests.opsmanager.managedcert_helper import ManagedCertificateNames, _write_appdb_ca, cert_manager
from tests.opsmanager.om_external_appdb_test_helpers import (
    MANAGED_CERTIFICATE_OWNER_UID_LABEL,
    assert_managed_appdb_graph_owner,
    assert_sentinel_doc_present,
    managed_appdb_graph,
    managed_appdb_member_graph,
    wait_for_managed_appdb_graph_owner,
    without_managed_certificate_owner_uid,
    write_sentinel_doc,
)

FORWARD_OM_NAME = "forward-managed-mc"
FORWARD_META_OM_NAME = "forward-managed-mc-meta"
FORWARD_MEMBER_COUNTS: list[int | None] = [2, 1]
APP_DB_CA_BUNDLE_HASH_ANNOTATION = "appDBCABundleHash"


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    config = get_multi_cluster_operator_installation_config(namespace)
    config["security.managedCertificate.enabled"] = "true"
    return get_multi_cluster_operator(
        namespace,
        get_central_cluster_name(),
        config,
        get_central_cluster_client(),
        get_member_cluster_clients(),
        get_member_cluster_names(),
        create_managed_certificate_roles=True,
    )


@fixture(scope="module")
def meta_om(
    multi_cluster_operator: Operator,
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta_om.yaml"), name=FORWARD_META_OM_NAME, namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.update()
    resource.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
    resource.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
    return resource


@fixture(scope="module")
def primary_om(
    multi_cluster_operator: Operator,
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    clusters = get_member_cluster_names()[:2]
    assert len(clusters) == 2, "managed forward migration requires two member clusters"
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), name=FORWARD_OM_NAME, namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.create_admin_secret(api_client=central_cluster_client)
    resource.allow_mdb_rc_versions()
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = cluster_spec_list(clusters, [1, 1])
    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "version": custom_appdb_version,
        "clusterSpecList": cluster_spec_list(clusters, FORWARD_MEMBER_COUNTS),
        "security": {"managedCertificate": {}},
    }
    resource["spec"]["configuration"]["mongodb.release.autoDownload.enterprise"] = "true"
    resource.update()
    return resource


@fixture(scope="module")
def external_appdb(
    primary_om: MongoDBOpsManager,
    meta_om: MongoDBOpsManager,
    namespace: str,
    custom_mdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi-cluster.yaml"), name=primary_om.app_db_name(), namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["role"] = "AppDB"
    resource["spec"]["persistent"] = True
    resource["spec"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names()[:2], FORWARD_MEMBER_COUNTS)
    resource["spec"]["security"] = {"managedCertificate": {}}
    configure_appdb_role_mongodb_multi(resource, meta_om, namespace)
    return resource


def _appdb_member_cluster_names(ops_manager: MongoDBOpsManager) -> list[str]:
    return [spec["clusterName"] for _, spec in ops_manager.get_appdb_indexed_cluster_spec_items()]


def _om_appdb_ca_bundle_hashes(ops_manager: MongoDBOpsManager) -> dict:
    return {
        spec["clusterName"]: ops_manager.read_statefulset(spec["clusterName"]).spec.template.metadata.annotations.get(
            APP_DB_CA_BUNDLE_HASH_ANNOTATION
        )
        for _, spec in ops_manager.get_om_indexed_cluster_spec_items()
    }


@pytest.mark.e2e_om_external_multi_cluster_appdb_forward_managedcert
class TestMultiClusterManagedOpsManagerForwardMigration:
    appdb_graph: ClassVar[dict] = {}
    member_graphs: ClassVar[dict] = {}
    om_pod_uids: ClassVar[set] = set()
    ca_bundle_hashes: ClassVar[dict] = {}
    connection_url: ClassVar[str] = ""

    def test_deploy_internal_managed_appdb(self, primary_om: MongoDBOpsManager):
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)

    def test_capture_state(self, primary_om: MongoDBOpsManager, tmp_path: Path):
        primary_om.load()
        names = ManagedCertificateNames.for_ops_manager(primary_om)
        self.__class__.appdb_graph = managed_appdb_graph(primary_om.namespace, names.app_db_name)
        assert_managed_appdb_graph_owner(self.appdb_graph, primary_om)
        self.__class__.member_graphs = {
            cluster: managed_appdb_member_graph(primary_om.namespace, names, get_member_cluster_api_client(cluster))
            for cluster in _appdb_member_cluster_names(primary_om)
        }
        bundle_key = f"ConfigMap/{names.app_db_ca_bundle_config_map}"
        for cluster, graph in self.member_graphs.items():
            entry = graph[bundle_key]
            if cluster == get_central_cluster_name():
                # The operator's own cluster holds the source ConfigMap: it is owned by a reference and
                # has no owner label.
                assert any(
                    ref["kind"] == "MongoDBOpsManager" and ref["name"] == primary_om.name for ref in entry["owners"]
                ), cluster
            else:
                assert entry["labels"].get(MANAGED_CERTIFICATE_OWNER_UID_LABEL) == primary_om["metadata"]["uid"], cluster
        self.__class__.om_pod_uids = {pod.metadata.uid for _, pod in primary_om.read_om_pods()}
        assert self.om_pod_uids
        self.__class__.ca_bundle_hashes = _om_appdb_ca_bundle_hashes(primary_om)
        assert all(self.ca_bundle_hashes.values()), self.ca_bundle_hashes
        self.__class__.connection_url = read_appdb_connection_url(
            primary_om, _appdb_member_cluster_names(primary_om)[0]
        )
        write_sentinel_doc(
            self.connection_url,
            tls_ca_file=_write_appdb_ca(primary_om.namespace, tmp_path / "appdb-ca.pem", names.app_db_name),
        )

    def test_external_pending_without_reference(self, primary_om: MongoDBOpsManager, external_appdb: MongoDBMulti):
        external_appdb.create()
        external_appdb.assert_reaches_phase(
            Phase.Pending, msg_regexp="Cannot take ownership of the AppDB Statefulset", timeout=600
        )
        names = ManagedCertificateNames.for_ops_manager(primary_om)
        assert managed_appdb_graph(primary_om.namespace, names.app_db_name) == self.appdb_graph

    def test_set_external_ref_and_wait_for_handover(self, primary_om: MongoDBOpsManager, external_appdb: MongoDBMulti):
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = {
            "name": external_appdb.name,
            "kind": "MongoDBMultiCluster",
        }
        primary_om.update()
        external_appdb.assert_reaches_phase(Phase.Running, timeout=1800)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
        primary_om.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)

    def test_handover_preserves_graph_and_credentials(
        self, primary_om: MongoDBOpsManager, external_appdb: MongoDBMulti, tmp_path: Path
    ):
        external_appdb.load()
        names = ManagedCertificateNames.for_ops_manager(primary_om)
        wait_for_managed_appdb_graph_owner(primary_om.namespace, names.app_db_name, external_appdb)
        graph = managed_appdb_graph(primary_om.namespace, names.app_db_name)
        assert set(graph) == set(self.appdb_graph)
        for key, before in self.appdb_graph.items():
            assert graph[key]["uid"] == before["uid"], key
            # The owner UID label is an ownership marker, so it is the one part of the spec that has
            # to change: assert_managed_appdb_graph_owner below pins it to the database's UID.
            assert without_managed_certificate_owner_uid(graph[key]["spec"]) == without_managed_certificate_owner_uid(
                before["spec"]
            ), key
            assert graph[key]["data"] == before["data"], key
        assert_managed_appdb_graph_owner(graph, external_appdb)
        assert read_appdb_connection_url(primary_om, _appdb_member_cluster_names(primary_om)[0]) == self.connection_url
        assert_sentinel_doc_present(
            self.connection_url,
            tls_ca_file=_write_appdb_ca(primary_om.namespace, tmp_path / "appdb-ca.pem", names.app_db_name),
        )

    def test_handover_preserves_member_copies(self, primary_om: MongoDBOpsManager, external_appdb: MongoDBMulti):
        names = ManagedCertificateNames.for_ops_manager(primary_om)
        db_uid = external_appdb["metadata"]["uid"]
        bundle_key = f"ConfigMap/{names.app_db_ca_bundle_config_map}"
        for cluster in _appdb_member_cluster_names(primary_om):
            api_client = get_member_cluster_api_client(cluster)
            central = cluster == get_central_cluster_name()

            def owned_by_database(api_client=api_client, central=central) -> bool:
                entry = managed_appdb_member_graph(primary_om.namespace, names, api_client)[bundle_key]
                if central:
                    # The source ConfigMap moves from an Ops Manager owner reference to the database's.
                    return any(ref["uid"] == db_uid for ref in entry["owners"])
                return entry["labels"].get(MANAGED_CERTIFICATE_OWNER_UID_LABEL) == db_uid

            KubernetesTester.wait_until(
                owned_by_database,
                timeout=600,
                sleep_time=5,
                msg=f"managed AppDB bundle ownership in cluster {cluster}",
            )
            graph = managed_appdb_member_graph(primary_om.namespace, names, api_client)
            before = self.member_graphs[cluster]
            assert set(graph) == set(before), cluster
            for key, entry in before.items():
                assert graph[key]["uid"] == entry["uid"], f"{cluster}: {key}"
                assert graph[key]["data"] == entry["data"], f"{cluster}: {key}"

    def test_primary_om_did_not_roll(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        assert {pod.metadata.uid for _, pod in primary_om.read_om_pods()} == self.om_pod_uids

    def test_ca_bundle_hash_unchanged(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        hashes = _om_appdb_ca_bundle_hashes(primary_om)
        assert hashes == self.ca_bundle_hashes
        assert all(hashes.values())

    def test_handover_markers_cleared(self, primary_om: MongoDBOpsManager, external_appdb: MongoDBMulti):
        assert_no_appdb_migration_annotations(external_appdb, _appdb_member_cluster_names(primary_om))
