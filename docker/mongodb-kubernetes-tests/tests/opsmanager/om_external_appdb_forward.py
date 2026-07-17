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
    meta_om_resource,
    password_secret_name,
    read_om_pod_restart_counts,
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
  2. TestSentinelDocSurvivesForwardMigration - Procedure 2 (Forward Migration from an existing
     internal AppDB)
  3. TestReverseMigrationAfterForwardMigration - Procedure 3, continuing from that migrated state
  4. TestAdoptionGateBlocksWithoutBothSignals - the two-signal adoption gate, on its own MongoDB CR
     (no Primary OM involvement)

See om_external_appdb_fresh.py for Procedure 1 (Fresh Start) and the same Procedure 3 logic
starting from a fresh-started state instead.

NOTE ON EXECUTION: this suite was authored against the implementation plan and verified only via
static checks (Python syntax / import resolution). It has NOT been run against a live kind cluster -
that must happen separately (see mck-dev:local-kind-dev), e.g.:

    pytest -m e2e_om_external_appdb_forward -v
"""

OM_NAME = "om-external-appdb-fwd"
DB_NAME = f"{OM_NAME}-db"  # must match the operator's required "<om-name>-db" naming convention
GATE_DB_NAME = "om-external-appdb-gate-db"


@fixture(scope="module")
def meta_om(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    return meta_om_resource(namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def primary_om(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary.yaml"), name=OM_NAME, namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
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


@fixture(scope="module")
def gate_appdb_mongodb(
    namespace: str,
    custom_mdb_version: str,
    member_cluster_names,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDB:
    resource = appdb_role_resource(
        namespace,
        custom_mdb_version,
        name=GATE_DB_NAME,
        member_cluster_names=member_cluster_names,
        central_cluster_client=central_cluster_client,
    )
    try_load(resource)
    return resource


@pytest.mark.e2e_om_external_appdb_forward
class TestDeployMetaOpsManager:
    """Deploys the management-plane Ops Manager the AppDB-role MongoDB CRs are configured against."""

    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_om_external_appdb_forward
class TestSentinelDocSurvivesForwardMigration:
    """Procedure 2: start with internal AppDB, write a sentinel doc, create the MongoDB (role: AppDB)
    CR named "<om-name>-db", set externalApplicationDatabaseRef, and wait for adoption. Restart-count
    assertion is unconditional (not "at most one") because Open Item 1 in the design doc has been
    verified for the default-port case by Task 0's spike: AppDBSpec.BuildConnectionURL and
    MongoDB.BuildConnectionString produce identical output, so the computed connection string value
    does not change across the switch and no connectionStringHash-triggered restart is expected.
    The non-default-port case remains an open caveat and is out of scope for this fixture (default port)."""

    restart_counts_before: ClassVar[dict[str, int]]
    password_secret_before: ClassVar[dict[str, str]]

    def test_create_om_with_internal_appdb(self, primary_om: MongoDBOpsManager):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_write_sentinel_doc(self, primary_om: MongoDBOpsManager):
        write_sentinel_doc(primary_om.read_appdb_connection_url())

    def test_capture_state_before_migration(self, primary_om: MongoDBOpsManager, namespace: str):
        self.__class__.restart_counts_before = read_om_pod_restart_counts(primary_om)
        self.__class__.password_secret_before = read_secret(namespace, password_secret_name(OM_NAME))

    def test_create_appdb_role_mongodb(self, external_appdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(external_appdb, meta_om, namespace)
        external_appdb.update()

    def test_set_external_appdb_ref(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = {"name": DB_NAME, "kind": ref_kind_for_appdb()}
        primary_om.update()

    def test_adoption_gate_clears_and_appdb_reaches_running(self, external_appdb: MongoDB):
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_om_reaches_running(self, primary_om: MongoDBOpsManager):
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_sentinel_doc_survives(self, primary_om: MongoDBOpsManager):
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url())

    def test_no_om_pod_restarts_across_switch(self, primary_om: MongoDBOpsManager):
        restart_counts_after = read_om_pod_restart_counts(primary_om)
        assert restart_counts_after == self.restart_counts_before, (
            "Primary OM pod(s) restarted across the forward migration switch; "
            "AppDBSpec.BuildConnectionURL and MongoDB.BuildConnectionString were expected to "
            "produce an identical connection-string value for this default-port fixture "
            "(see design doc Resolved Open Item 1)"
        )

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

    def test_reverse_migration_reconfigure_om(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        # update() sends a JSON merge patch: a locally deleted key is absent from the patch body
        # and the server keeps it - only an explicit null removes the field
        primary_om["spec"]["externalApplicationDatabaseRef"] = None
        primary_om.update()

    def test_internal_appdb_management_resumes(self, primary_om: MongoDBOpsManager):
        # recreate-from-scratch: the new StatefulSet re-binds the retained PVCs by name
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_sentinel_doc_survives_reverse_migration(self, primary_om: MongoDBOpsManager):
        # the data-preservation proof: written before the forward migration, survives CR deletion
        # and the recreate because the PVCs were retained
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url())


@pytest.mark.e2e_om_external_appdb_forward
class TestAdoptionGateBlocksWithoutBothSignals:
    """The two-signal adoption gate (checkAdoptionGate in mongodbreplicaset_controller.go) must
    block takeover of a foreign StatefulSet unless BOTH the appDBMigrationReadyAnnotation is
    present AND the foreign OwnerReference is gone. Neither signal alone is sufficient.

    No Primary OM in this class: the gate only needs a foreign StatefulSet plus an AppDB-role CR
    with a resolvable project. ProcessValidationsOnReconcile -> project.ReadConfigAndCredentials ->
    connection.PrepareOpsManagerConnection -> ensureAppDBRoleUser all run in
    ReplicaSetReconcilerHelper.Reconcile BEFORE checkAdoptionGate
    (mongodbreplicaset_controller.go:355-389), and that project resolves against the Meta OM."""

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

    def test_create_foreign_statefulset_no_annotation(self, namespace: str):
        k8s_client.AppsV1Api().create_namespaced_stateful_set(
            namespace, self._minimal_statefulset(GATE_DB_NAME, namespace)
        )

    def test_create_appdb_role_mongodb(self, gate_appdb_mongodb: MongoDB, meta_om: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(gate_appdb_mongodb, meta_om, namespace)
        gate_appdb_mongodb.update()

    def test_reports_waiting_status_without_annotation(self, gate_appdb_mongodb: MongoDB):
        gate_appdb_mongodb.assert_reaches_phase(Phase.Pending, msg_regexp=".*waiting for Ops Manager.*", timeout=120)

    def test_statefulset_untouched_without_annotation(self, namespace: str):
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(GATE_DB_NAME, namespace)
        assert len(sts.metadata.owner_references) == 1
        assert sts.metadata.owner_references[0].uid == self.FOREIGN_OWNER_UID

    def test_add_annotation_but_keep_foreign_owner_reference(self, namespace: str):
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(GATE_DB_NAME, namespace)
        sts.metadata.annotations = sts.metadata.annotations or {}
        sts.metadata.annotations["mongodb.com/appdb-migration-ready"] = "true"
        k8s_client.AppsV1Api().replace_namespaced_stateful_set(GATE_DB_NAME, namespace, sts)

    def test_still_blocked_with_annotation_but_foreign_owner_present(self, gate_appdb_mongodb: MongoDB, namespace: str):
        gate_appdb_mongodb.load()
        gate_appdb_mongodb.update()
        gate_appdb_mongodb.assert_reaches_phase(Phase.Pending, msg_regexp=".*waiting for Ops Manager.*", timeout=120)
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(GATE_DB_NAME, namespace)
        assert len(sts.metadata.owner_references) == 1, "gate must not adopt while foreign OwnerReference remains"
