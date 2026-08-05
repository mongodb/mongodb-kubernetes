from typing import ClassVar, Optional

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
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.opsmanager.om_external_appdb_test_helpers import (
    appdb_role_resource,
    appdb_tls_security,
    assert_no_migration_annotations,
    assert_project_exists,
    assert_sentinel_doc_present,
    configure_appdb_role_mongodb,
    meta_om_resource,
    password_secret_name,
    ref_kind_for_appdb,
    write_sentinel_doc,
)

"""
E2E test coverage for External AppDB via MongoDB CR reference:
  - Procedure 2: Forward Migration (from an existing internal AppDB)
  - Procedure 3: Reverse Migration (fallback path - delete MongoDB CR first)

The classes form one continuous story on a single Primary OM, in order:
  1. TestDeployInitialState - Prerequisite: deploy the management plane (Meta OM) and
     the Primary OM with internal AppDB
  2. TestSentinelDocSurvivesForwardMigration - Procedure 2: Forward Migration from an
     existing internal AppDB to an external AppDB (MongoDB CR with role: AppDB)
  3. TestReverseMigrationAfterForwardMigration - Procedure 3: Reverse Migration (fallback
     path) — the MongoDB CR is deleted FIRST, then the OM is reconfigured to internal AppDB.
     The StatefulSet and shared secrets are garbage-collected; the AppDB is recreated from
     scratch with retained PVCs.

See om_external_appdb_fresh.py for Procedure 1 (Fresh Start) and Procedure 3 (graceful
reverse migration — the MongoDB CR is kept and the OM adopts the STS).
"""

OM_NAME = "primary-om"
DB_NAME = f"{OM_NAME}-db"  # must match the operator's required "<om-name>-db" naming convention


@fixture(scope="module")
def meta_om(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    return meta_om_resource(namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def appdb_ca_configmap(app_db_issuer_ca_configmap: str) -> str:
    # same CA used by the internal AppDB (before forward migration) and the external MongoDB CR
    # (after), so the computed connection string is identical across the switch and OM pods don't roll.
    return app_db_issuer_ca_configmap


@fixture(scope="module")
def appdb_cert_prefix(namespace: str, issuer: str) -> str:
    # "appdb-<DB_NAME>-cert"; the internal AppDB and the MongoDB CR share DB_NAME and this prefix, so
    # they resolve to the same member cert secret across forward migration.
    return create_appdb_certs(namespace, issuer, DB_NAME)


@fixture(scope="module")
def primary_om(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    appdb_ca_configmap: str,
    appdb_cert_prefix: str,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_external_appdb_primary_om.yaml"), namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    # start with a TLS-enabled internal AppDB so that after forward migration to the (also TLS)
    # external CR the connection string (ssl=true, same hosts) is unchanged.
    resource["spec"]["applicationDatabase"]["security"] = appdb_tls_security(appdb_ca_configmap, appdb_cert_prefix)
    try_load(resource)
    return resource


@fixture(scope="module")
def external_appdb(namespace: str, custom_mdb_version: str, appdb_cert_prefix: str, appdb_ca_configmap: str) -> MongoDB:
    resource = appdb_role_resource(namespace, custom_mdb_version, name=DB_NAME)
    resource.configure_custom_tls(appdb_ca_configmap, appdb_cert_prefix)
    try_load(resource)
    return resource


@pytest.mark.e2e_om_external_appdb_forward
class TestDeployInitialState:
    """Deploys the management-plane Ops Manager and the primary Ops Manager with internal AppDB."""

    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_om_with_internal_appdb(self, primary_om: MongoDBOpsManager):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_om_external_appdb_forward
class TestSentinelDocSurvivesForwardMigration:
    """Procedure 2: start with internal AppDB, write a sentinel doc, create the MongoDB (role: AppDB)
    CR named "<om-name>-db", set externalApplicationDatabaseRef, and wait for adoption."""

    password_secret_before: ClassVar[dict[str, str]]
    connection_string_before: ClassVar[str]

    def test_write_sentinel_doc(self, primary_om: MongoDBOpsManager, issuer_ca_filepath: str):
        cnx_string = primary_om.read_appdb_connection_url()
        # the internal AppDB is TLS-enabled, so its connection string must request TLS
        assert "ssl=true" in cnx_string
        write_sentinel_doc(cnx_string, tls_ca_file=issuer_ca_filepath)

    def test_capture_state_before_migration(self, primary_om: MongoDBOpsManager, namespace: str):
        self.__class__.password_secret_before = read_secret(namespace, password_secret_name(OM_NAME))
        self.__class__.connection_string_before = primary_om.read_appdb_connection_url()

    def test_create_external_appdb(self, external_appdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(external_appdb, meta_om, namespace)
        external_appdb.update()

    def test_external_appdb_pending_before_ref_set(self, external_appdb: MongoDB):
        # the STS already exists (owned by the OM's internal AppDB); the AppDB CR can't
        # adopt it until the OM sets externalApplicationDatabaseRef and detaches it
        external_appdb.assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Cannot take ownership of the AppDB Statefulset",
            timeout=300,
        )

    def test_set_external_appdb_ref(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = {"name": DB_NAME, "kind": ref_kind_for_appdb()}
        primary_om.update()

    def test_external_appdb_reaches_running(self, external_appdb: MongoDB):
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_om_reaches_running(self, primary_om: MongoDBOpsManager):
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_no_migration_annotations_after_forward_migration(self, namespace: str):
        assert_no_migration_annotations(namespace, DB_NAME)

    def test_sentinel_doc_survives(self, primary_om: MongoDBOpsManager, issuer_ca_filepath: str):
        cnx_string = primary_om.read_appdb_connection_url()
        # after forward migration the external CR is also TLS, so the string still requests TLS
        assert "ssl=true" in cnx_string
        assert_sentinel_doc_present(cnx_string, tls_ca_file=issuer_ca_filepath)

    def test_connection_string_unchanged_after_forward_migration(self, primary_om: MongoDBOpsManager):
        # same hosts + same password => the computed connection string value must not
        # change across the forward migration (and therefore OM pods never roll)
        assert primary_om.read_appdb_connection_url() == self.connection_string_before

    def test_password_secret_unchanged_after_forward_migration(self, namespace: str):
        password_secret_now = read_secret(namespace, password_secret_name(OM_NAME))
        assert password_secret_now == self.password_secret_before


@pytest.mark.e2e_om_external_appdb_forward
class TestReverseMigrationAfterForwardMigration:
    """Procedure 3 v2 fallback path, continuing from TestSentinelDocSurvivesForwardMigration's end
    state (a completed Forward Migration): the MongoDB CR is deleted FIRST - plain Kubernetes
    deletion, no finalizer. The StatefulSet and shared secrets are garbage-collected and the AppDB
    (and OM) take a downtime window; the retained PVCs preserve the data. Reconfiguring the OM
    afterwards recreates the AppDB from scratch, re-binding the PVCs - the sentinel document must
    survive. Credential rotation is an accepted property of this path, so no password/keyfile
    stability is asserted."""

    def test_reverse_migration_delete_mongodb_first(self, external_appdb: MongoDB, namespace: str):
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

    def test_statefulset_garbage_collected(self, namespace: str):
        # plain deletion: the CR-owned StatefulSet goes with it; the OM in external mode reports
        # Failed on ref validation during the gap (tolerated, not asserted)
        def sts_is_gone():
            try:
                k8s_client.AppsV1Api().read_namespaced_stateful_set(DB_NAME, namespace)
                return False
            except ApiException as e:
                if e.status == 404:
                    return True
                raise

        KubernetesTester.wait_until(sts_is_gone, timeout=300)

    def test_shared_secrets_garbage_collected(self, namespace: str):
        # the CR's OwnerReference on the shared secrets triggers their GC on CR deletion
        for name in [password_secret_name(OM_NAME), f"{DB_NAME}-keyfile"]:

            def secret_is_gone():
                try:
                    k8s_client.CoreV1Api().read_namespaced_secret(name, namespace)
                    return False
                except ApiException as e:
                    if e.status == 404:
                        return True
                    raise

            KubernetesTester.wait_until(secret_is_gone, timeout=300)

    def test_reverse_migration_reconfigure_om(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        # update() sends a JSON merge patch: a locally deleted key is absent from the patch body
        # and the server keeps it - only an explicit null removes the field
        primary_om["spec"]["externalApplicationDatabaseRef"] = None
        primary_om.update()

    def test_internal_appdb_management_resumes(self, primary_om: MongoDBOpsManager):
        # recreate-from-scratch: the new StatefulSet re-binds the retained PVCs by name
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)

    def test_no_migration_annotations_after_reverse_migration(self, namespace: str):
        assert_no_migration_annotations(namespace, DB_NAME)

    def test_sentinel_doc_survives_reverse_migration(self, primary_om: MongoDBOpsManager, issuer_ca_filepath: str):
        # the data-preservation proof: written before the forward migration, survives CR deletion
        # and the recreate because the PVCs were retained. The internal AppDB security (TLS) is
        # inherited from the OM spec's applicationDatabase, so the reconnect is over TLS.
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url(), tls_ca_file=issuer_ca_filepath)

    def test_project_still_exists_after_reverse_migration(self, meta_om: MongoDBOpsManager):
        assert_project_exists(meta_om, DB_NAME)
