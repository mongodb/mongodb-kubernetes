from typing import ClassVar, Optional

import kubernetes.client
import pymongo
import pytest
from kubernetes import client as k8s_client
from kubernetes.client.rest import ApiException
from kubetester import read_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.opsmanager.om_external_appdb_test_helpers import (
    appdb_role_resource,
    assert_owned_by_mongodb,
    assert_owned_by_ops_manager,
    assert_sentinel_doc_present,
    configure_appdb_role_mongodb,
    meta_om_resource,
    password_secret_name,
    ref_kind_for_appdb,
    write_sentinel_doc,
)

"""
E2E test coverage for External AppDB via MongoDB CR reference:
  - Procedure 1: Fresh Start
  - Procedure 3: Reverse Migration (graceful)

The classes form one continuous story on a single Primary OM, in order:
  1. TestDeployMetaOpsManager - Prerequisite: deploy the management plane (Meta OM)
  2. TestFreshStartExternalAppDB - Procedure 1: Fresh Start (the Primary OM CR has no
     spec.applicationDatabase and never had an internal AppDB)
  3. TestReverseMigrationAfterFreshStart - Procedure 3: Reverse Migration (graceful) back to internal AppDB
     handled by the Primary OM CR spec.applicationDatabase. Here we first remove
     spec.externalApplicationDatabaseRef, add spec.applicationDatabase and wait for Primary OM to
     migrate to internal AppDB. After that we delete detached MongoDB CR (External AppDB).

See om_external_appdb_forward.py for Procedure 2 (Forward Migration) and the Procedure 3 (Reverse Migration)
with significant difference: For reverse migration we first remove the MongoDB CR (External AppDB) and test
if the Primary OM can adopt the internal AppDB.
"""
OM_NAME = "primary-om-with-external-appdb"
DB_NAME = f"{OM_NAME}-db"  # must match the operator's required "<om-name>-db" naming convention


@fixture(scope="module")
def meta_om(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    return meta_om_resource(namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def primary_om(namespace: str, custom_version: Optional[str]) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary_om_no_appdb.yaml"),
        namespace=namespace,
    )
    resource.set_version(custom_version)
    # the ref stays in code rather than the fixture yaml: its kind is dynamic
    # (MongoDBMultiCluster in multi-cluster runs)
    resource["spec"]["externalApplicationDatabaseRef"] = {"name": DB_NAME, "kind": ref_kind_for_appdb()}
    try_load(resource)
    return resource


@fixture(scope="module")
def external_appdb(
    namespace: str,
    custom_mdb_version: str,
    member_cluster_names,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDB:
    resource = appdb_role_resource(
        namespace,
        custom_mdb_version,
        name=DB_NAME,
        member_cluster_names=member_cluster_names,
        central_cluster_client=central_cluster_client,
    )
    try_load(resource)
    return resource


@pytest.mark.e2e_om_external_appdb_fresh
class TestDeployMetaOpsManager:
    """Deploys the management-plane Ops Manager the AppDB-role MongoDB CRs are configured against."""

    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_om_external_appdb_fresh
class TestFreshStartExternalAppDB:
    """Procedure 1: create the External AppDB (MongoDB role: AppDB) CR managed by Meta OM, then create the Primary
    OM CR with spec.externalApplicationDatabaseRef set from the start and no spec.applicationDatabase -
    no internal AppDB ever exists for this OM CR."""

    def test_create_appdb_role_mongodb(self, external_appdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(external_appdb, meta_om, namespace)
        external_appdb.update()
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_om_with_ref_and_no_internal_appdb(self, primary_om: MongoDBOpsManager):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_external_appdb_statefulset_created(self, namespace: str):
        # the StatefulSet belongs solely to the MongoDB CR - the OM controller must never have
        # touched its ownership in the fresh-start flow
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(DB_NAME, namespace)
        assert_owned_by_mongodb(sts.metadata, DB_NAME)

    def test_password_secret(self, namespace: str):
        # ownership follows the AppDB's manager: in the fresh start the MongoDB CR created the
        # shared password secret, so it must carry the CR's OwnerReference
        sec = k8s_client.CoreV1Api().read_namespaced_secret(password_secret_name(OM_NAME), namespace)
        assert_owned_by_mongodb(sec.metadata, DB_NAME)
        assert "password" in read_secret(namespace, password_secret_name(OM_NAME))

    def test_connection_string_secret(self, primary_om: MongoDBOpsManager, namespace: str):
        # the connection-string secret is created by the OM controller (never by the referenced
        # CR, per the design) and intentionally carries no OwnerReferences
        cnx_string = primary_om.read_appdb_connection_url()
        expected_hosts = {f"{DB_NAME}-{i}.{DB_NAME}-svc.{namespace}.svc.cluster.local:27017" for i in range(3)}

        # the URI itself must name the referenced CR's pods - not merely "work" via server discovery
        parsed = pymongo.uri_parser.parse_uri(cnx_string)
        assert {f"{host}:{port}" for host, port in parsed["nodelist"]} == expected_hosts

        client = pymongo.MongoClient(cnx_string, serverSelectionTimeoutMS=30000)
        try:
            hello = client.admin.command("hello")
            assert hello["setName"] == DB_NAME
            assert set(hello["hosts"]) == expected_hosts
        finally:
            client.close()


@pytest.mark.e2e_om_external_appdb_fresh
class TestReverseMigrationAfterFreshStart:
    """Procedure 3: Reconfiguring the OM (remove spec.externalApplicationDatabaseRef, add
    spec.applicationDatabase) triggers the release handshake; the MongoDB CR is deleted only after the
    handover completes, and must not disturb anything the OM now owns."""

    connection_string_before: ClassVar[str]
    shared_secret_uids: ClassVar[dict[str, str]]
    shared_secret_data: ClassVar[dict[str, dict]]

    # every shared handover secret the OM claims at adoption; all of them would be
    # garbage-collected by the CR deletion if the ownership transfer failed
    SHARED_SECRET_NAMES = [f"{DB_NAME}-om-password", f"{DB_NAME}-keyfile"]

    def test_write_sentinel_doc(self, primary_om: MongoDBOpsManager):
        write_sentinel_doc(primary_om.read_appdb_connection_url())

    def test_capture_secrets_before_reverse_migration(self, primary_om: MongoDBOpsManager, namespace: str):
        # graceful path: same hosts + same password => the computed connection string value must
        # never change (and therefore Primary OM pods never roll)
        self.__class__.connection_string_before = primary_om.read_appdb_connection_url()
        secrets = {
            name: k8s_client.CoreV1Api().read_namespaced_secret(name, namespace) for name in self.SHARED_SECRET_NAMES
        }
        self.__class__.shared_secret_uids = {name: s.metadata.uid for name, s in secrets.items()}
        self.__class__.shared_secret_data = {name: s.data for name, s in secrets.items()}

    def _assert_shared_secrets_claimed_and_unchanged(self, namespace: str):
        for name in self.SHARED_SECRET_NAMES:
            sec = k8s_client.CoreV1Api().read_namespaced_secret(name, namespace)
            assert sec.metadata.uid == self.shared_secret_uids[name], f"secret {name} was recreated"
            assert sec.data == self.shared_secret_data[name], f"secret {name} contents changed"
            assert_owned_by_ops_manager(sec.metadata, OM_NAME)

    def test_reverse_migration_reconfigure_om(self, primary_om: MongoDBOpsManager, custom_appdb_version: str):
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = None
        primary_om["spec"]["applicationDatabase"] = {"members": 3, "version": custom_appdb_version}
        primary_om.update()

    def test_external_appdb_is_unmanaged(self, external_appdb: MongoDB):
        # the released message shows only in the short window before the OM adopts; afterwards
        # the adoption gate reports the "unmanaged" message - both prove the release happened
        external_appdb.assert_reaches_phase(
            Phase.Pending,
            msg_regexp=".*(unmanaged|AppDB StatefulSet to Ops Manager).*",
            timeout=300,
        )

    def test_internal_appdb_management_resumes(self, primary_om: MongoDBOpsManager):
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_internal_appdb_statefulset_created(self, namespace: str):
        # after adoption the StatefulSet belongs solely to the Ops Manager
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(DB_NAME, namespace)
        assert_owned_by_ops_manager(sts.metadata, OM_NAME)

    def test_om_secrets_only_updated_owner_reference(self, primary_om: MongoDBOpsManager, namespace: str):
        # the OM-claimed shared secrets must survive the handover with identical contents; only
        # their ownership changed (MongoDB CR -> Ops Manager)
        self._assert_shared_secrets_claimed_and_unchanged(namespace)
        assert primary_om.read_appdb_connection_url() == self.connection_string_before

    def test_delete_mongodb_cr_after_handover(self, external_appdb: MongoDB, namespace: str):
        external_appdb.delete()

        def cr_is_gone():
            try:
                k8s_client.CustomObjectsApi().get_namespaced_custom_object(
                    "mongodb.com", "v1", namespace, "mongodb", DB_NAME
                )
                return False
            except ApiException as e:
                if e.status == 404:
                    return True
                raise

        KubernetesTester.wait_until(cr_is_gone, timeout=300)

    def test_om_unaffected_by_cr_deletion(self, primary_om: MongoDBOpsManager, namespace: str):
        # the deletion is post-handover cleanup: statuses must not dip, the shared secrets and
        # the connection string must be untouched
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=120)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=120)
        self._assert_shared_secrets_claimed_and_unchanged(namespace)
        assert primary_om.read_appdb_connection_url() == self.connection_string_before

    def test_sentinel_doc_survives_reverse_migration(self, primary_om: MongoDBOpsManager):
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url())
