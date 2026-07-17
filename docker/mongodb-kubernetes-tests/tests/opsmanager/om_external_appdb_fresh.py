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
from tests.opsmanager.external_appdb_helpers import (
    appdb_role_resource,
    assert_sentinel_doc_present,
    configure_appdb_role_mongodb,
    meta_om_resource,
    password_secret_name,
    ref_kind_for_appdb,
    write_sentinel_doc,
)

"""
e2e coverage for "external AppDB via MongoDB CR reference"
(docs/superpowers/specs/2026-07-02-appdb-mongodb-cr-reference-design.md).

Topology follows the external-AppDB spike: a management-plane "Meta OM" (meta-om) owns the
projects that manage the AppDB-role MongoDB CRs; the Primary OM under test never manages its own
AppDB (that would make the AppDB's automation agents depend on the very OM whose availability
depends on that AppDB).

The classes form one continuous story on a single Primary OM, in order:
  1. TestDeployMetaOpsManager - deploy the management plane
  2. TestFreshStartExternalAppDB - Procedure 1 (true Fresh Start: the Primary OM CR has no
     spec.applicationDatabase and never had an internal AppDB)
  3. TestReverseMigrationAfterFreshStart - Procedure 3, continuing from that fresh-started state

See om_external_appdb_forward.py for Procedure 2 (Forward Migration) and the same Procedure 3
logic starting from a completed forward migration instead, plus the two-signal adoption gate.

NOTE ON EXECUTION: this suite was authored against the implementation plan and verified only via
static checks (Python syntax / import resolution). It has NOT been run against a live kind cluster -
that must happen separately (see mck-dev:local-kind-dev), e.g.:

    pytest -m e2e_om_external_appdb_fresh -v
"""
OM_NAME = "primary-om-with-external-appdb"
DB_NAME = f"{OM_NAME}-db"  # must match the operator's required "<om-name>-db" naming convention


@fixture(scope="module")
def meta_om(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    return meta_om_resource(namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def primary_om(namespace: str, custom_version: Optional[str]) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("primary_om_with_external_appdb.yaml"),
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
    """Procedure 1: create the MongoDB (role: AppDB) CR managed by Meta OM, then create the Primary
    OM CR with externalApplicationDatabaseRef set from the start and no spec.applicationDatabase -
    no internal AppDB ever exists for this OM CR."""

    def test_create_appdb_role_mongodb(self, external_appdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(external_appdb, meta_om, namespace)
        external_appdb.update()
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_om_with_ref_and_no_internal_appdb(self, primary_om: MongoDBOpsManager):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_no_internal_appdb_statefulset_created(self, namespace: str):
        # the referenced CR's StatefulSet (created by the MongoDB controller) is the only one with
        # this name; the OM controller must never have created an internal AppDB StatefulSet of its
        # own - assert the StatefulSet is not owned by the OM CR
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(DB_NAME, namespace)
        owner_kinds = {ref.kind for ref in (sts.metadata.owner_references or [])}
        assert "MongoDBOpsManager" not in owner_kinds

    def test_shared_password_secret_exists(self, namespace: str):
        secret = read_secret(namespace, password_secret_name(OM_NAME))
        assert "password" in secret

    def test_fixed_connection_string_secret_has_working_uri(self, primary_om: MongoDBOpsManager, namespace: str):
        cnx_string = primary_om.read_appdb_connection_url()
        expected_hosts = {f"{DB_NAME}-{i}.{DB_NAME}-svc.{namespace}.svc.cluster.local:27017" for i in range(3)}

        # the URI itself must name the referenced CR's pods - not merely "work" via server discovery
        parsed = pymongo.uri_parser.parse_uri(cnx_string)
        assert {f"{host}:{port}" for host, port in parsed["nodelist"]} == expected_hosts

        client = pymongo.MongoClient(cnx_string, serverSelectionTimeoutMS=30000)
        try:
            # a real, authenticated command against the referenced MongoDB CR through OM's fixed
            # secret - raises on connectivity/auth failure; the assertions prove we reached the
            # referenced CR's replica set with the expected topology
            hello = client.admin.command("hello")
            assert hello["setName"] == DB_NAME
            assert set(hello["hosts"]) == expected_hosts
        finally:
            client.close()


@pytest.mark.e2e_om_external_appdb_fresh
class TestReverseMigrationAfterFreshStart:
    """Procedure 3 v2 (graceful), continuing from TestFreshStartExternalAppDB's end state: the
    MongoDB CR is NOT deleted to start the migration. Reconfiguring the OM (remove ref, add
    spec.applicationDatabase) triggers the release handshake; the CR is deleted only after the
    handover completes, and must not disturb anything the OM now owns."""

    password_secret_before: ClassVar[dict[str, str]]
    shared_secret_uids: ClassVar[dict[str, str]]

    # every shared handover secret the OM claims at adoption; all of them would be
    # garbage-collected by the CR deletion if the ownership transfer failed
    SHARED_SECRET_NAMES = [f"{DB_NAME}-om-password", f"{DB_NAME}-keyfile"]

    def test_write_sentinel_doc(self, primary_om: MongoDBOpsManager):
        write_sentinel_doc(primary_om.read_appdb_connection_url())

    def test_capture_secrets_before_reverse_migration(self, namespace: str):
        self.__class__.password_secret_before = read_secret(namespace, password_secret_name(OM_NAME))
        self.__class__.shared_secret_uids = {
            name: k8s_client.CoreV1Api().read_namespaced_secret(name, namespace).metadata.uid
            for name in self.SHARED_SECRET_NAMES
        }

    def test_reverse_migration_reconfigure_om(self, primary_om: MongoDBOpsManager, custom_appdb_version: str):
        # v2: the MongoDB CR stays; reconfiguring the OM alone starts the release handshake.
        # update() sends a JSON merge patch: only an explicit null removes a field
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = None
        primary_om["spec"]["applicationDatabase"] = {"members": 3, "version": custom_appdb_version}
        primary_om.update()

    def test_mongodb_cr_reaches_released_state(self, external_appdb: MongoDB):
        # the released message shows only in the short window between the CR stripping its
        # ownerReference and the OM adopting the StatefulSet; afterwards the adoption gate reports
        # the generalized "managed by Ops Manager" message - both prove the release happened
        external_appdb.assert_reaches_phase(
            Phase.Pending,
            msg_regexp=".*(AppDB StatefulSet to Ops Manager|is managed by Ops Manager).*",
            timeout=300,
        )

    def test_internal_appdb_management_resumes(self, primary_om: MongoDBOpsManager):
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

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

    def test_om_untouched_by_cr_deletion(self, primary_om: MongoDBOpsManager, namespace: str):
        # every OM-claimed shared secret must survive the CR deletion (same object, not recreated)
        for name in self.SHARED_SECRET_NAMES:
            sec = k8s_client.CoreV1Api().read_namespaced_secret(name, namespace)
            assert sec.metadata.uid == self.shared_secret_uids[name], f"secret {name} was recreated"
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)

    def test_sentinel_doc_survives_reverse_migration(self, primary_om: MongoDBOpsManager):
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url())

    def test_password_secret_unchanged_after_reverse_migration(self, namespace: str):
        # graceful-path property: shared password identical across the whole handover
        password_secret_now = read_secret(namespace, password_secret_name(OM_NAME))
        assert password_secret_now == self.password_secret_before
