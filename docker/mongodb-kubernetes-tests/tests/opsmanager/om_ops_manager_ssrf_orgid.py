from typing import Optional

from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

# SECBUG-4043: end-to-end replication and regression guard for the "orgId" SSRF.
#
# The operator reads the "orgId" value verbatim from the user-editable project ConfigMap
# and splices it into the Ops Manager REST path (controllers/om/omclient.go, ReadOrganization
# and siblings) via fmt.Sprintf without url.PathEscape. An attacker who can edit the ConfigMap
# therefore controls the whole URI suffix after "/orgs/" and can inject query strings, fragments,
# extra path segments or ".." traversal into the operator's digest-authenticated request,
# redirecting it to arbitrary OM endpoints.
#
# This test provisions a real Ops Manager, reaches a clean baseline, then patches the project
# ConfigMap's orgId to "<real-org-id>?injected=pwned" and asserts the SECURE outcome (the CR fails
# because the payload is neutralised into a single bogus org id). It therefore:
#   * FAILS on the vulnerable operator (OM ignores the injected query, the real org resolves and the
#     CR stays Running) -- that red run is the concrete repro, and
#   * PASSES on the fixed operator (orgId is url.PathEscape'd, OM 404s, the CR reports the error).

PROJECT_NAME = "mdb"
CONFIG_MAP_NAME = f"{PROJECT_NAME}-config"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=PROJECT_NAME,
    ).configure(ops_manager, PROJECT_NAME)
    resource.set_version(custom_mdb_version)

    try_load(resource)
    return resource


@mark.e2e_om_ops_manager_ssrf_orgid
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_ops_manager_ssrf_orgid
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    # Baseline: the project ConfigMap has an empty orgId, so the operator creates/uses the "mdb"
    # organization and the resource reconciles cleanly against the real Ops Manager.
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@mark.e2e_om_ops_manager_ssrf_orgid
def test_forge_request_via_orgid_injection(namespace: str, replica_set: MongoDB):
    # Resolve the real org id created by the baseline reconcile (org name == project name).
    org_id = replica_set.get_om_tester().api_get_organization_id(PROJECT_NAME)
    assert org_id, f"expected the baseline reconcile to create organization {PROJECT_NAME!r}"

    # Attacker payload: a valid org id with an injected query string.
    #   * Vulnerable operator: GET /orgs/<id>?injected=pwned -- OM resolves the real org and ignores
    #     the unknown query param, so the CR stays Running (the injection is accepted and forwarded).
    #   * Fixed operator: GET /orgs/<id>%3Finjected=pwned -- the payload is one escaped path segment,
    #     OM 404s and the CR fails with "organization with id ... not found".
    payload = f"{org_id}?injected=pwned"
    KubernetesTester.patch_config_map(namespace, CONFIG_MAP_NAME, {"orgId": payload})
    print(f"Patched ConfigMap {CONFIG_MAP_NAME!r}: orgId -> {payload!r} (SECBUG-4043 injection)")

    # Asserting the secure outcome makes this a regression test: red on the vulnerable build
    # (stays Running), green on the fixed build.
    replica_set.assert_reaches_phase(
        Phase.Failed,
        msg_regexp=".*organization with id.*not found.*",
        timeout=300,
    )
