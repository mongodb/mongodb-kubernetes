from typing import ClassVar, Optional

import kubernetes.client
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
    read_om_pod_restart_counts,
    ref_kind_for_appdb,
    write_sentinel_doc,
)

"""
e2e coverage for "external AppDB via MongoDB CR reference"
(docs/superpowers/specs/2026-07-02-appdb-mongodb-cr-reference-design.md).

Covers, starting from an existing internal AppDB:
  - Procedure 2 (Forward Migration): TestSentinelDocSurvivesForwardMigration
  - Procedure 3 (Reverse Migration), starting from that completed forward migration:
    TestReverseMigrationAfterForwardMigration
plus the two-signal adoption gate every procedure depends on (only load-bearing during forward
migration/takeover, so it lives here rather than in om_external_appdb_fresh.py):
  - TestAdoptionGateBlocksWithoutBothSignals

See om_external_appdb_fresh.py for Procedure 1 (Fresh Start), the same Procedure 3 logic starting
from a fresh-started state instead, and the "<om-name>-db" naming convention test.

NOTE ON EXECUTION: this suite was authored against the implementation plan and verified only via
static checks (Python syntax / import resolution). It has NOT been run against a live kind cluster -
that must happen separately (see mck-dev:local-kind-dev), e.g.:

    pytest -m e2e_om_external_appdb -v
"""


@pytest.mark.e2e_om_external_appdb
class TestSentinelDocSurvivesForwardMigration:
    """Procedure 2: start with internal AppDB, write a sentinel doc, create the MongoDB (role: AppDB)
    CR named "<om-name>-db", set externalApplicationDatabaseRef, and wait for adoption. Restart-count
    assertion is unconditional (not "at most one") because Open Item 1 in the design doc has been
    verified for the default-port case by Task 0's spike: AppDBSpec.BuildConnectionURL and
    MongoDB.BuildConnectionString produce identical output, so the computed connection string value
    does not change across the switch and no connectionStringHash-triggered restart is expected.
    The non-default-port case remains an open caveat and is out of scope for this fixture (default port)."""

    restart_counts_before: ClassVar[dict[str, int]]

    OM_NAME = "om-external-appdb-fwd"
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

    def test_create_om_with_internal_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_write_sentinel_doc(self, ops_manager: MongoDBOpsManager):
        write_sentinel_doc(ops_manager.read_appdb_connection_url())

    def test_capture_om_pod_restart_counts_before_migration(self, ops_manager: MongoDBOpsManager):
        self.__class__.restart_counts_before = read_om_pod_restart_counts(ops_manager)

    def test_create_appdb_role_mongodb(self, appdb_mongodb: MongoDB, ops_manager: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(appdb_mongodb, ops_manager, namespace)
        appdb_mongodb.update()

    def test_set_external_appdb_ref(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["externalApplicationDatabaseRef"] = {"name": self.DB_NAME, "kind": ref_kind_for_appdb()}
        ops_manager.update()

    def test_adoption_gate_clears_and_appdb_reaches_running(self, appdb_mongodb: MongoDB):
        appdb_mongodb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_om_reaches_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_sentinel_doc_survives(self, ops_manager: MongoDBOpsManager):
        assert_sentinel_doc_present(ops_manager.read_appdb_connection_url())

    def test_no_om_pod_restarts_across_switch(self, ops_manager: MongoDBOpsManager):
        restart_counts_after = read_om_pod_restart_counts(ops_manager)
        assert restart_counts_after == self.restart_counts_before, (
            "Primary OM pod(s) restarted across the forward migration switch; "
            "AppDBSpec.BuildConnectionURL and MongoDB.BuildConnectionString were expected to "
            "produce an identical connection-string value for this default-port fixture "
            "(see design doc Resolved Open Item 1)"
        )


@pytest.mark.e2e_om_external_appdb
class TestReverseMigrationAfterForwardMigration:
    """Procedure 3, starting from a completed Forward Migration (Procedure 2): delete the MongoDB CR
    and remove externalApplicationDatabaseRef together, assert the CR stays terminating until the
    appdb-detach finalizer's cleanup completes, then assert internal AppDB management resumes, the
    sentinel doc survives, and the shared password secret's value is unchanged throughout."""

    password_secret_before: ClassVar[dict[str, str]]

    OM_NAME = "om-external-appdb-rev"
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

    def test_create_om_with_internal_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_write_sentinel_doc(self, ops_manager: MongoDBOpsManager):
        write_sentinel_doc(ops_manager.read_appdb_connection_url())

    def test_capture_password_secret_before_migration(self, namespace: str):
        self.__class__.password_secret_before = read_secret(namespace, password_secret_name(self.OM_NAME))

    def test_forward_migration(self, appdb_mongodb: MongoDB, ops_manager: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(appdb_mongodb, ops_manager, namespace)
        appdb_mongodb.update()

        ops_manager.load()
        ops_manager["spec"]["externalApplicationDatabaseRef"] = {"name": self.DB_NAME, "kind": ref_kind_for_appdb()}
        ops_manager.update()

        appdb_mongodb.assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_password_secret_unchanged_after_forward_migration(self, namespace: str):
        password_secret_now = read_secret(namespace, password_secret_name(self.OM_NAME))
        assert password_secret_now == self.password_secret_before

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
class TestAdoptionGateBlocksWithoutBothSignals:
    """The two-signal adoption gate (checkAdoptionGate in mongodbreplicaset_controller.go) must
    block takeover of a foreign StatefulSet unless BOTH the appDBMigrationReadyAnnotation is
    present AND the foreign OwnerReference is gone. Neither signal alone is sufficient."""

    OM_NAME = "om-external-appdb-gate"
    DB_NAME = f"{OM_NAME}-db"
    FOREIGN_OWNER_UID = "11111111-1111-1111-1111-111111111111"

    @staticmethod
    def _foreign_owner_reference() -> k8s_client.V1OwnerReference:
        return k8s_client.V1OwnerReference(
            api_version="v1",
            kind="ConfigMap",
            name="some-unrelated-owner",
            uid=TestAdoptionGateBlocksWithoutBothSignals.FOREIGN_OWNER_UID,
            controller=True,
            block_owner_deletion=True,
        )

    @staticmethod
    def _minimal_statefulset(name: str, namespace: str, annotations: Optional[dict] = None) -> k8s_client.V1StatefulSet:
        labels = {"app": name}
        return k8s_client.V1StatefulSet(
            metadata=k8s_client.V1ObjectMeta(
                name=name,
                namespace=namespace,
                annotations=annotations or {},
                owner_references=[TestAdoptionGateBlocksWithoutBothSignals._foreign_owner_reference()],
            ),
            spec=k8s_client.V1StatefulSetSpec(
                service_name=name,
                replicas=0,
                selector=k8s_client.V1LabelSelector(match_labels=labels),
                template=k8s_client.V1PodTemplateSpec(
                    metadata=k8s_client.V1ObjectMeta(labels=labels),
                    spec=k8s_client.V1PodSpec(containers=[k8s_client.V1Container(name="placeholder", image="busybox")]),
                ),
            ),
        )

    @fixture(scope="class")
    def ops_manager(
        self, namespace: str, custom_version: Optional[str], custom_appdb_version: str
    ) -> MongoDBOpsManager:
        # A real, running OM CR is required here: ProcessValidationsOnReconcile ->
        # project.ReadConfigAndCredentials -> connection.PrepareOpsManagerConnection ->
        # ensureAppDBRoleUser all run in ReplicaSetReconcilerHelper.Reconcile BEFORE
        # checkAdoptionGate (mongodbreplicaset_controller.go:355-389) - without a resolvable
        # project ConfigMap/credentials secret, reconcile fails before ever reaching the gate,
        # and this test would never actually exercise checkAdoptionGate at all.
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

    def test_create_om_with_internal_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_create_foreign_statefulset_no_annotation(self, namespace: str):
        k8s_client.AppsV1Api().create_namespaced_stateful_set(
            namespace, self._minimal_statefulset(self.DB_NAME, namespace)
        )

    def test_create_appdb_role_mongodb(self, appdb_mongodb: MongoDB, ops_manager: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(appdb_mongodb, ops_manager, namespace)
        appdb_mongodb.update()

    def test_reports_waiting_status_without_annotation(self, appdb_mongodb: MongoDB):
        appdb_mongodb.assert_reaches_phase(Phase.Pending, msg_regexp=".*waiting for Ops Manager.*", timeout=120)

    def test_statefulset_untouched_without_annotation(self, namespace: str):
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(self.DB_NAME, namespace)
        assert len(sts.metadata.owner_references) == 1
        assert sts.metadata.owner_references[0].uid == self.FOREIGN_OWNER_UID

    def test_add_annotation_but_keep_foreign_owner_reference(self, namespace: str):
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(self.DB_NAME, namespace)
        sts.metadata.annotations = sts.metadata.annotations or {}
        sts.metadata.annotations["mongodb.com/appdb-migration-ready"] = "true"
        k8s_client.AppsV1Api().replace_namespaced_stateful_set(self.DB_NAME, namespace, sts)

    def test_still_blocked_with_annotation_but_foreign_owner_present(self, appdb_mongodb: MongoDB, namespace: str):
        appdb_mongodb.load()
        appdb_mongodb.update()
        appdb_mongodb.assert_reaches_phase(Phase.Pending, msg_regexp=".*waiting for Ops Manager.*", timeout=120)
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(self.DB_NAME, namespace)
        assert len(sts.metadata.owner_references) == 1, "gate must not adopt while foreign OwnerReference remains"
