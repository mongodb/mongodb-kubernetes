from typing import ClassVar, Optional

import kubernetes.client
import pytest
from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
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
    assert_no_internal_appdb_statefulset,
    configure_appdb_role_mongodb_multi,
    multi_cluster_appdb_role_resource,
    primary_om_internal_appdb_resource,
    primary_om_resource,
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
    namespace: str, custom_version: str, central_cluster_client: kubernetes.client.ApiClient
) -> MongoDBOpsManager:
    resource = primary_om_resource(namespace, custom_version)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def internal_primary_om(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    appdb_ca_configmap: str,
    appdb_cert_prefix: str,
    appdb_member_cluster_names: list[str],
) -> MongoDBOpsManager:
    return primary_om_internal_appdb_resource(
        namespace,
        custom_version,
        custom_appdb_version,
        appdb_ca_configmap,
        appdb_cert_prefix,
        appdb_member_cluster_names,
    )


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
    resource = multi_cluster_appdb_role_resource(
        namespace,
        custom_mdb_version,
        appdb_cert_prefix,
        appdb_ca_configmap,
        appdb_member_cluster_names,
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_fresh
class TestDeployMetaOpsManager:
    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_fresh
class TestFreshStartExternalAppDB:
    def test_create_appdb_role_mongodb(
        self,
        external_appdb: MongoDBMulti,
        meta_om: MongoDBOpsManager,
        namespace: str,
        appdb_ca_configmap: str,
    ):
        configure_appdb_role_mongodb_multi(external_appdb, meta_om, namespace, appdb_ca_configmap)
        external_appdb.update()
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_om_with_ref_and_no_internal_appdb(
        self,
        primary_om: MongoDBOpsManager,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)

        with pytest.raises(ApiException, match=r"NotFound|not found"):
            assert_no_internal_appdb_statefulset(namespace, APPDB_NAME, central_cluster_client)

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


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_fresh
class TestReverseMigrationAfterFreshStart:
    connection_string_before: ClassVar[str]
    shared_secret_uids: ClassVar[dict[str, str]]
    shared_secret_data: ClassVar[dict[str, dict]]

    SHARED_SECRET_NAMES = [f"{APPDB_NAME}-om-password", f"{APPDB_NAME}-keyfile"]

    def test_write_sentinel_doc(self, primary_om: MongoDBOpsManager, issuer_ca_filepath: str):
        write_sentinel_doc(primary_om.read_appdb_connection_url(), tls_ca_file=issuer_ca_filepath)

    def test_capture_secrets_before_reverse_migration(self, primary_om: MongoDBOpsManager, namespace: str):
        self.__class__.connection_string_before = primary_om.read_appdb_connection_url()
        secrets = {
            name: kubernetes.client.CoreV1Api().read_namespaced_secret(name, namespace)
            for name in self.SHARED_SECRET_NAMES
        }
        self.__class__.shared_secret_uids = {name: secret.metadata.uid for name, secret in secrets.items()}
        self.__class__.shared_secret_data = {name: secret.data for name, secret in secrets.items()}

    def _assert_shared_secrets_claimed_and_unchanged(self, primary_om: MongoDBOpsManager, namespace: str):
        for name in self.SHARED_SECRET_NAMES:
            secret = kubernetes.client.CoreV1Api().read_namespaced_secret(name, namespace)
            assert secret.metadata.uid == self.shared_secret_uids[name], f"secret {name} was recreated"
            assert secret.data == self.shared_secret_data[name], f"secret {name} contents changed"
            assert secret.metadata.owner_references, f"secret {name} must be owned by Ops Manager"
            assert secret.metadata.owner_references[0].kind == "MongoDBOpsManager"
            assert secret.metadata.owner_references[0].name == primary_om.name

    def test_reverse_migration_reconfigure_om(
        self,
        primary_om: MongoDBOpsManager,
        internal_primary_om: MongoDBOpsManager,
    ):
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = None
        primary_om["spec"]["topology"] = internal_primary_om["spec"]["topology"]
        primary_om["spec"]["clusterSpecList"] = internal_primary_om["spec"]["clusterSpecList"]
        primary_om["spec"]["applicationDatabase"] = internal_primary_om["spec"]["applicationDatabase"]
        primary_om.update()

    def test_external_appdb_is_unmanaged(self, external_appdb: MongoDBMulti):
        external_appdb.assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Cannot take ownership of the AppDB Statefulset: Configure spec.externalApplicationDatabaseRef under Ops Manager CR or delete this resource",
            timeout=300,
        )

    def test_internal_appdb_management_resumes(self, primary_om: MongoDBOpsManager):
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_internal_appdb_statefulset_created(
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

    def test_om_secrets_only_updated_owner_reference(self, primary_om: MongoDBOpsManager, namespace: str):
        self._assert_shared_secrets_claimed_and_unchanged(primary_om, namespace)
        assert primary_om.read_appdb_connection_url() == self.connection_string_before

    def test_delete_mongodb_cr_after_handover(self, external_appdb: MongoDBMulti, namespace: str):
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

    def test_om_unaffected_by_cr_deletion(self, primary_om: MongoDBOpsManager, namespace: str):
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=120)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=120)
        assert primary_om.read_appdb_connection_url() == self.connection_string_before

    def test_sentinel_doc_survives_reverse_migration(self, primary_om: MongoDBOpsManager, issuer_ca_filepath: str):
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url(), tls_ca_file=issuer_ca_filepath)

    def test_project_still_exists_after_reverse_migration(self, meta_om: MongoDBOpsManager):
        assert_project_exists(meta_om, APPDB_NAME)
