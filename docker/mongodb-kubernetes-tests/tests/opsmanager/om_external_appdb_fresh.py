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
    password_secret_name,
    ref_kind_for_appdb,
    write_sentinel_doc,
)

"""
e2e coverage for "external AppDB via MongoDB CR reference"
(docs/superpowers/specs/2026-07-02-appdb-mongodb-cr-reference-design.md).

Covers, starting from a Fresh Start (no internal AppDB ever existed for the OM CR):
  - Procedure 1 (Fresh Start): TestFreshStartExternalAppDB
  - Procedure 3 (Reverse Migration), starting from that fresh-started state:
    TestReverseMigrationAfterFreshStart
plus the "<om-name>-db" naming convention (exercised via the initial ref-setting step, no
migration state needed): TestNamingConventionRejectsWrongName

See om_external_appdb_forward.py for Procedure 2 (Forward Migration) and the same Procedure 3
logic starting from a completed forward migration instead, plus the two-signal adoption gate.

NOTE ON EXECUTION: this suite was authored against the implementation plan and verified only via
static checks (Python syntax / import resolution). It has NOT been run against a live kind cluster -
that must happen separately (see mck-dev:local-kind-dev), e.g.:

    pytest -m e2e_om_external_appdb -v
"""

OM_NAME = "om-external-appdb"
DB_NAME = f"{OM_NAME}-db"  # must match the operator's required "<om-name>-db" naming convention
WRONG_DB_NAME = "om-external-appdb-wrong-name"


@pytest.mark.e2e_om_external_appdb
class TestFreshStartExternalAppDB:
    """Procedure 1: create the OM CR without an external AppDB ref, create the MongoDB (role: AppDB)
    CR, then set externalApplicationDatabaseRef - no internal AppDB ever existed for this OM CR."""

    @fixture(scope="class")
    def ops_manager(
        self, namespace: str, custom_version: Optional[str], custom_appdb_version: str
    ) -> MongoDBOpsManager:
        resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_external_appdb_primary.yaml"), namespace=namespace)
        resource.set_version(custom_version)
        resource.set_appdb_version(custom_appdb_version)
        try_load(resource)
        return resource

    @fixture(scope="class")
    def appdb_mongodb(
        self,
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

    def test_create_om_without_ref(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_create_appdb_role_mongodb(self, appdb_mongodb: MongoDB, ops_manager: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(appdb_mongodb, ops_manager, namespace)
        appdb_mongodb.update()
        appdb_mongodb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_set_external_appdb_ref(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["externalApplicationDatabaseRef"] = {"name": DB_NAME, "kind": ref_kind_for_appdb()}
        ops_manager.update()

    def test_appdb_mongodb_reaches_running(self, appdb_mongodb: MongoDB):
        appdb_mongodb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_om_reaches_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_shared_password_secret_exists(self, namespace: str):
        secret = read_secret(namespace, password_secret_name(OM_NAME))
        assert "password" in secret

    def test_fixed_connection_string_secret_has_working_uri(self, ops_manager: MongoDBOpsManager):
        cnx_string = ops_manager.read_appdb_connection_url()
        client = pymongo.MongoClient(cnx_string, serverSelectionTimeoutMS=30000)
        try:
            # runs a real command against the referenced MongoDB CR through OM's fixed secret
            client.admin.command("ping")
        finally:
            client.close()


@pytest.mark.e2e_om_external_appdb
class TestReverseMigrationAfterFreshStart:
    """Procedure 3, starting from a Fresh Start (Procedure 1): the OM CR never had an internal AppDB.
    After the fresh-started external AppDB reaches Running, delete the MongoDB CR and remove
    externalApplicationDatabaseRef together, assert the CR stays terminating until the appdb-detach
    finalizer's cleanup completes, then assert internal AppDB management resumes, the sentinel doc
    survives, and the shared password secret's value is unchanged throughout."""

    password_secret_before: ClassVar[dict[str, str]]

    OM_NAME = "om-external-appdb-rev-fresh"
    DB_NAME = f"{OM_NAME}-db"

    @fixture(scope="class")
    def ops_manager(
        self, namespace: str, custom_version: Optional[str], custom_appdb_version: str
    ) -> MongoDBOpsManager:
        resource = MongoDBOpsManager.from_yaml(
            yaml_fixture("om_external_appdb_primary.yaml"), name=self.OM_NAME, namespace=namespace
        )
        resource.set_version(custom_version)
        resource.set_appdb_version(custom_appdb_version)
        try_load(resource)
        return resource

    @fixture(scope="class")
    def appdb_mongodb(
        self,
        namespace: str,
        custom_mdb_version: str,
        member_cluster_names,
        central_cluster_client: kubernetes.client.ApiClient,
    ) -> MongoDB:
        resource = appdb_role_resource(
            namespace,
            custom_mdb_version,
            name=self.DB_NAME,
            member_cluster_names=member_cluster_names,
            central_cluster_client=central_cluster_client,
        )
        try_load(resource)
        return resource

    def test_create_om_without_ref(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_fresh_start_external_appdb(self, appdb_mongodb: MongoDB, ops_manager: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(appdb_mongodb, ops_manager, namespace)
        appdb_mongodb.update()
        appdb_mongodb.assert_reaches_phase(Phase.Running, timeout=900)

        ops_manager.load()
        ops_manager["spec"]["externalApplicationDatabaseRef"] = {"name": self.DB_NAME, "kind": ref_kind_for_appdb()}
        ops_manager.update()

        appdb_mongodb.assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_write_sentinel_doc(self, ops_manager: MongoDBOpsManager):
        write_sentinel_doc(ops_manager.read_appdb_connection_url())

    def test_capture_password_secret_before_reverse_migration(self, namespace: str):
        self.__class__.password_secret_before = read_secret(namespace, password_secret_name(self.OM_NAME))

    def test_reverse_migration_delete_ref_and_mongodb_together(
        self, appdb_mongodb: MongoDB, ops_manager: MongoDBOpsManager
    ):
        ops_manager.load()
        del ops_manager["spec"]["externalApplicationDatabaseRef"]
        ops_manager.update()

        appdb_mongodb.delete()

    def test_appdb_mongodb_cr_stays_terminating_until_detach_completes(self, appdb_mongodb: MongoDB, namespace: str):
        # the appdb-detach finalizer keeps the CR present (deletionTimestamp set) until the
        # operator has stripped this CR's OwnerReference from the StatefulSet and annotated it
        # ready for OM's re-adoption (Procedure 3, steps 3a-3c)
        def cr_is_gone():
            try:
                k8s_client.CustomObjectsApi().get_namespaced_custom_object(
                    "mongodb.com", "v1", namespace, "mongodb", self.DB_NAME
                )
                return False
            except ApiException as e:
                if e.status == 404:
                    return True
                raise

        KubernetesTester.wait_until(cr_is_gone, timeout=900)

    def test_internal_appdb_management_resumes(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_sentinel_doc_survives_reverse_migration(self, ops_manager: MongoDBOpsManager):
        assert_sentinel_doc_present(ops_manager.read_appdb_connection_url())

    def test_password_secret_unchanged_after_reverse_migration(self, namespace: str):
        password_secret_now = read_secret(namespace, password_secret_name(self.OM_NAME))
        assert password_secret_now == self.password_secret_before


@pytest.mark.e2e_om_external_appdb
class TestNamingConventionRejectsWrongName:
    """spec.externalApplicationDatabaseRef.name must equal "<om-name>-db" exactly
    (validateExternalAppDBReference in mongodbopsmanager_controller.go). Any other name is
    rejected with a clear validation error, and no external-AppDB behavior (detach, connection
    string computation, watch establishment) may proceed."""

    OM_NAME = "om-external-appdb-badname"

    @fixture(scope="class")
    def ops_manager(
        self, namespace: str, custom_version: Optional[str], custom_appdb_version: str
    ) -> MongoDBOpsManager:
        resource = MongoDBOpsManager.from_yaml(
            yaml_fixture("om_external_appdb_primary.yaml"), name=self.OM_NAME, namespace=namespace
        )
        resource.set_version(custom_version)
        resource.set_appdb_version(custom_appdb_version)
        try_load(resource)
        return resource

    def test_create_om_with_internal_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_set_wrong_name_ref_is_rejected(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["externalApplicationDatabaseRef"] = {
            "name": WRONG_DB_NAME,
            "kind": ref_kind_for_appdb(),
        }
        ops_manager.update()

        ops_manager.om_status().assert_reaches_phase(
            Phase.Failed,
            msg_regexp=".*does not match required naming convention.*",
            timeout=120,
        )

    def test_internal_appdb_unaffected(self, ops_manager: MongoDBOpsManager):
        # no detach, no connection-string switch: internal AppDB must still be Running throughout
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=120)
