from typing import ClassVar, Optional

import kubernetes.client
from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import APPDB_NAME
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    appdb_ca_configmap as helper_appdb_ca_configmap,
)
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    appdb_cert_prefix as helper_appdb_cert_prefix,
)
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    appdb_member_cluster_names as default_appdb_member_cluster_names,
)
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    appdb_statefulset,
    assert_multi_cluster_appdb_statefulset_identity,
    assert_no_appdb_migration_annotations,
    configure_appdb_role_mongodb_multi,
    primary_om_internal_appdb_resource,
    read_appdb_connection_url,
)
from tests.opsmanager.om_external_appdb_test_helpers import (
    assert_project_exists,
    assert_sentinel_doc_present,
    write_sentinel_doc,
)


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return default_appdb_member_cluster_names()


@fixture(scope="module")
def meta_om(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta_om.yaml"), name="meta-om", namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    appdb_ca_configmap: str,
    appdb_cert_prefix: str,
    appdb_member_cluster_names: list[str],
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource = primary_om_internal_appdb_resource(
        namespace,
        custom_version,
        custom_appdb_version,
        appdb_ca_configmap,
        appdb_cert_prefix,
        appdb_member_cluster_names,
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.allow_mdb_rc_versions()
    resource.create_admin_secret(api_client=central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def appdb_ca_configmap(multi_cluster_issuer_ca_configmap: str) -> str:
    return helper_appdb_ca_configmap(multi_cluster_issuer_ca_configmap)


@fixture(scope="module")
def appdb_cert_prefix(namespace: str, multi_cluster_issuer: str) -> str:
    return helper_appdb_cert_prefix(namespace, multi_cluster_issuer)


@fixture(scope="module")
def external_appdb(
    namespace: str,
    custom_mdb_version: str,
    appdb_cert_prefix: str,
    appdb_ca_configmap: str,
    appdb_member_cluster_names: list[str],
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi-cluster.yaml"), name=APPDB_NAME, namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["role"] = "AppDB"
    resource["spec"]["persistent"] = True
    resource["spec"]["clusterSpecList"] = cluster_spec_list(appdb_member_cluster_names, [2, 2])
    resource.configure_custom_tls(appdb_ca_configmap, appdb_cert_prefix)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_forward
class TestDeployMetaOpsManager:
    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_forward
class TestDeployPrimaryOpsManagerWithInternalAppDB:
    def test_create_om_with_internal_appdb(self, primary_om: MongoDBOpsManager):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_forward
class TestForwardMigrationToExternalAppDB:
    connection_string_before: ClassVar[str]

    def test_write_sentinel_doc(
        self,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
        issuer_ca_filepath: str,
    ):
        cnx_string = read_appdb_connection_url(primary_om, appdb_member_cluster_names[0])
        assert "ssl=true" in cnx_string
        write_sentinel_doc(cnx_string, tls_ca_file=issuer_ca_filepath)

    def test_capture_connection_string_before_migration(
        self, primary_om: MongoDBOpsManager, appdb_member_cluster_names: list[str]
    ):
        self.__class__.connection_string_before = read_appdb_connection_url(primary_om, appdb_member_cluster_names[0])

    def test_create_appdb_role_mongodb(
        self,
        external_appdb: MongoDBMulti,
        meta_om: MongoDBOpsManager,
        namespace: str,
        appdb_ca_configmap: str,
    ):
        configure_appdb_role_mongodb_multi(external_appdb, meta_om, namespace, appdb_ca_configmap)
        external_appdb.update()

    def test_external_appdb_pending_before_ref_set(self, external_appdb: MongoDBMulti):
        external_appdb.assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Cannot take ownership of the AppDB Statefulset",
            timeout=300,
        )

    def test_set_external_appdb_ref(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = {"name": APPDB_NAME, "kind": "MongoDBMultiCluster"}
        primary_om.update()

    def test_external_appdb_reaches_running(self, external_appdb: MongoDBMulti):
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_om_reaches_running(self, primary_om: MongoDBOpsManager):
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_no_migration_annotations_after_forward_migration(
        self,
        external_appdb: MongoDBMulti,
        appdb_member_cluster_names: list[str],
    ):
        assert_no_appdb_migration_annotations(external_appdb, appdb_member_cluster_names)

    def test_external_appdb_statefulsets_are_adopted(
        self,
        external_appdb: MongoDBMulti,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
    ):
        assert_multi_cluster_appdb_statefulset_identity(
            external_appdb,
            appdb_member_cluster_names,
        )

    def test_sentinel_doc_survives_forward_migration(
        self,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
        issuer_ca_filepath: str,
    ):
        cnx_string = read_appdb_connection_url(primary_om, appdb_member_cluster_names[0])
        assert "ssl=true" in cnx_string
        assert_sentinel_doc_present(cnx_string, tls_ca_file=issuer_ca_filepath)

    def test_connection_string_unchanged_after_forward_migration(
        self, primary_om: MongoDBOpsManager, appdb_member_cluster_names: list[str]
    ):
        assert read_appdb_connection_url(primary_om, appdb_member_cluster_names[0]) == self.connection_string_before


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_forward
class TestReverseMigrationAfterForwardMigration:
    def test_reverse_migration_delete_mongodb_first(self, external_appdb: MongoDBMulti, namespace: str):
        external_appdb.delete()

        def cr_is_gone():
            try:
                kubernetes.client.CustomObjectsApi().get_namespaced_custom_object(
                    "mongodb.com", "v1", namespace, "mongodbmulticluster", APPDB_NAME
                )
                return False
            except ApiException as e:
                if e.status == 404:
                    return True
                raise

        KubernetesTester.wait_until(cr_is_gone, timeout=300)

    def test_statefulsets_survive_cr_deletion(
        self,
        external_appdb: MongoDBMulti,
        appdb_member_cluster_names: list[str],
    ):
        expected_owner = f"{external_appdb.namespace}-{external_appdb.name}"
        for cluster_name in appdb_member_cluster_names:
            sts = appdb_statefulset(external_appdb, cluster_name)
            assert (
                not sts.metadata.owner_references
            ), f"AppDB StatefulSet {sts.metadata.name} in cluster {cluster_name} must have no ownerReferences, but got: {sts.metadata.owner_references}"
            assert (
                sts.metadata.labels.get("mongodbmulticluster") == expected_owner
            ), f"AppDB StatefulSet {sts.metadata.name} in cluster {cluster_name} must keep the multi-cluster owner label, but got labels: {sts.metadata.labels}"

    def test_reverse_migration_reconfigure_om(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = None
        primary_om.update()

    def test_internal_appdb_management_resumes(self, primary_om: MongoDBOpsManager):
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)

    def test_internal_appdb_statefulsets_are_reclaimed(
        self,
        external_appdb: MongoDBMulti,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
    ):
        for cluster_name in appdb_member_cluster_names:
            sts = appdb_statefulset(external_appdb, cluster_name)
            assert (
                not sts.metadata.owner_references
            ), f"AppDB StatefulSet {sts.metadata.name} in cluster {cluster_name} must have no ownerReferences, but got: {sts.metadata.owner_references}"
            assert (
                sts.metadata.labels.get("mongodb.com/v1.mongodbOpsManagerResourceOwner") == primary_om.name
            ), f"AppDB StatefulSet {sts.metadata.name} in cluster {cluster_name} must be owned by the Ops Manager, but got labels: {sts.metadata.labels}"
        assert_no_appdb_migration_annotations(external_appdb, appdb_member_cluster_names)

    def test_sentinel_doc_survives_reverse_migration(
        self,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
        issuer_ca_filepath: str,
    ):
        cnx_string = read_appdb_connection_url(primary_om, appdb_member_cluster_names[0])
        assert "ssl=true" in cnx_string
        assert_sentinel_doc_present(cnx_string, tls_ca_file=issuer_ca_filepath)

    def test_project_still_exists_after_reverse_migration(self, meta_om: MongoDBOpsManager):
        assert_project_exists(meta_om, APPDB_NAME)
