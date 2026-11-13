"""Leaf renewal and self-signed CA rotation for a MongoDBMultiCluster replica set with managed certificates.

The operator issues the certificates and publishes the trust bundles in its own cluster and copies the bundles
and the -pem secrets into every member cluster, where the pods mount them. A rotation is only safe when every
member cluster gets the widened bundle, with the previous CA retained, before any leaf moves to the new root, and
when a prune reaches every member cluster too. The members must roll onto the new files in every cluster
without their pods being recreated.

The last part rotates the server CA while the operator cannot reach one member cluster. The Istio
ServiceEntries that the disaster recovery tests use cut the operator off from that cluster's API server, so the
operator marks it as failed (automated failover is off, so it keeps the members where they are) and keeps
reconciling the others. Once the cluster is reachable again the operator must bring it up to date: the same
bundle as everywhere else, the re-issued leaves, and then a prune that reaches it too.
"""

from typing import List

import kubernetes
from kubetester import read_secret, try_load
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster.managedcert_rotation_mc_helper import (
    CA_BUNDLE_KEY,
    CLIENT,
    SERVER,
    allow_operator_to_reach,
    assert_certificate_renewals_reported,
    bundle_snapshot,
    ca_bundle_name,
    ca_certificate_name,
    leaf_certificates,
    pem_latest_hash,
    pod_uids_in_clusters,
    read_bundle_data,
    wait_for_automation_config_ca_files,
    wait_for_automation_config_names_files,
    wait_for_bundle_replicated,
    wait_for_certificate_revision_above,
    wait_for_failed_cluster,
    wait_for_leaf_signed_by,
    wait_for_pem_secret_rolled_in_clusters,
    wait_for_prune_annotation_removed,
)
from tests.opsmanager.managedcert_helper import (
    begin_ca_rotation,
    bundle_fingerprints,
    wait_for_bundle_pruned,
    wait_for_bundle_rotation,
    wait_for_ca_rotated,
)
from tests.tls.managedcert_helper import (
    assert_reachable_with_client_certificate,
    client_certificate_user,
    read_certificate_status,
    request_prune_previous_ca,
    trigger_certificate_renewal,
)

MDB_RESOURCE = "mc-rs-cert-rotation"
MEMBER_CLUSTER_MEMBERS = [1, 1, 1]

MEMBER_LEAF = f"{MDB_RESOURCE}-managed-cert"
CLUSTERFILE_LEAF = f"{MDB_RESOURCE}-managed-clusterfile"
AGENT_LEAF = f"{MDB_RESOURCE}-managed-agent-certs"
CLIENT_LEAVES = [CLUSTERFILE_LEAF, AGENT_LEAF]

CONNECTIVITY_ATTEMPTS = 5


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    """The operator with managed certificates enabled, installed after cert-manager.

    Automated failover is off: when the operator cannot reach a member cluster it only marks it as failed and
    keeps reconciling the others, instead of moving the members out of it, so the cluster can come back as it
    was.
    """
    operator_installation_config = get_multi_cluster_operator_installation_config(namespace)
    operator_installation_config["security.managedCertificate.enabled"] = "true"
    operator_installation_config["multiCluster.performFailOver"] = "false"
    return get_multi_cluster_operator(
        namespace,
        get_central_cluster_name(),
        operator_installation_config,
        get_central_cluster_client(),
        get_member_cluster_clients(),
        get_member_cluster_names(),
        create_managed_certificate_roles=True,
    )


@fixture(scope="module")
def mongodb_multi(
    cert_manager: str,
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, list(MEMBER_CLUSTER_MEMBERS))
    resource["spec"]["security"] = {
        "tls": {"enabled": True},
        "managedCertificate": {},
        "authentication": {"enabled": True, "modes": ["X509"], "internalCluster": "X509"},
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def app_user(mongodb_multi: MongoDBMulti, namespace: str) -> MongoDBUser:
    return client_certificate_user(mongodb_multi, namespace)


@fixture(scope="module")
def unreachable_cluster(member_cluster_clients: List[MultiClusterClient]) -> MultiClusterClient:
    """The member cluster the operator is cut off from in the last part of the module."""
    return member_cluster_clients[-1]


@fixture(scope="module")
def rotation() -> dict:
    """The state each step of the rotation hands to the next one."""
    return {}


def _pods(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> dict:
    return pod_uids_in_clusters(namespace, f"{MDB_RESOURCE}-", member_cluster_clients)


def _wait_for_automation_config_to_name_current_files(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    wait_for_automation_config_names_files(
        pem_latest_hash(namespace, f"{AGENT_LEAF}-pem", member_cluster_clients),
        {MDB_RESOURCE: pem_latest_hash(namespace, f"{MEMBER_LEAF}-pem", member_cluster_clients)},
        {MDB_RESOURCE: pem_latest_hash(namespace, f"{CLUSTERFILE_LEAF}-pem", member_cluster_clients)},
    )


def _rotate_server_ca(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> dict:
    """Records the server leaf, rotates the server CA and returns the state the next steps need."""
    state: dict = {
        "pods": _pods(namespace, member_cluster_clients),
        "leaf": read_secret(namespace, MEMBER_LEAF)["tls.crt"],
        "pem_hash": pem_latest_hash(namespace, f"{MEMBER_LEAF}-pem", member_cluster_clients),
        "client_leaves": leaf_certificates(namespace, CLIENT_LEAVES),
        "client_bundle": read_bundle_data(namespace, ca_bundle_name(MDB_RESOURCE, CLIENT)),
        "bundle_before": bundle_snapshot(namespace, MDB_RESOURCE, SERVER),
    }
    assert state["bundle_before"]["latest_hash"], "the server bundle must have a latestHash"

    ca_name = ca_certificate_name(MDB_RESOURCE, SERVER)
    state["old_ca"] = begin_ca_rotation(namespace, ca_name)
    state["new_ca"] = wait_for_ca_rotated(namespace, ca_name, state["old_ca"])

    widened = wait_for_bundle_rotation(
        namespace,
        ca_bundle_name(MDB_RESOURCE, SERVER),
        CA_BUNDLE_KEY,
        state["bundle_before"]["latest_hash"],
        state["bundle_before"]["published_at"],
    )
    assert set(widened["certificates"]) == set(bundle_fingerprints(state["old_ca"])) | set(
        bundle_fingerprints(state["new_ca"])
    ), "the widened server bundle must hold the previous and the new CA"
    state["widened"] = widened
    return state


def _assert_widened_bundle_replicated(namespace: str, state: dict, member_cluster_clients: List[MultiClusterClient]):
    replicated = wait_for_bundle_replicated(namespace, ca_bundle_name(MDB_RESOURCE, SERVER), member_cluster_clients)
    assert replicated["latestHash"] == state["widened"]["latest_hash"]
    assert (
        replicated["previousHash"] == state["bundle_before"]["latest_hash"]
    ), "every member cluster must know which bundle the widened one replaced"
    assert bundle_fingerprints(replicated[CA_BUNDLE_KEY]) == bundle_fingerprints(
        replicated[replicated["latestHash"]]
    ), f"the {CA_BUNDLE_KEY} key applications read must hold the latest bundle"


def _assert_rotation_completed(
    mongodb_multi: MongoDBMulti, namespace: str, state: dict, member_cluster_clients: List[MultiClusterClient]
):
    """The server leaf is on the new root in every member cluster, and the members rolled in place."""
    leaf = wait_for_leaf_signed_by(namespace, MEMBER_LEAF, state["leaf"], state["new_ca"])
    assert not cert_signed_by_bundle(leaf, state["old_ca"]), "the re-issued server leaf must not use the old CA"
    wait_for_pem_secret_rolled_in_clusters(namespace, f"{MEMBER_LEAF}-pem", state["pem_hash"], member_cluster_clients)

    assert (
        leaf_certificates(namespace, CLIENT_LEAVES) == state["client_leaves"]
    ), "a server CA rotation must not re-issue the client leaves"
    assert read_bundle_data(namespace, ca_bundle_name(MDB_RESOURCE, CLIENT)) == state["client_bundle"]

    wait_for_automation_config_ca_files(mongodb_multi, state["widened"]["latest_hash"])
    _wait_for_automation_config_to_name_current_files(namespace, member_cluster_clients)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)
    assert (
        _pods(namespace, member_cluster_clients) == state["pods"]
    ), "a CA rotation must not recreate the pods in any member cluster"
    # The previous CA stays trusted everywhere until it is pruned.
    _assert_widened_bundle_replicated(namespace, state, member_cluster_clients)


def _prune_server_ca(
    mongodb_multi: MongoDBMulti, namespace: str, state: dict, member_cluster_clients: List[MultiClusterClient]
):
    """Prunes the retained server CA and checks the narrowed bundle reaches every member cluster."""
    request_prune_previous_ca(mongodb_multi, SERVER)
    wait_for_prune_annotation_removed(mongodb_multi)

    pruned = wait_for_bundle_pruned(namespace, ca_bundle_name(MDB_RESOURCE, SERVER), CA_BUNDLE_KEY, state["new_ca"])
    assert set(pruned["certificates"]) == set(bundle_fingerprints(state["new_ca"]))

    replicated = wait_for_bundle_replicated(namespace, ca_bundle_name(MDB_RESOURCE, SERVER), member_cluster_clients)
    assert replicated["latestHash"] == pruned["latest_hash"]
    assert replicated.get("previousHash") == pruned["previous_hash"], "every cluster must hold the same previousHash"
    assert (
        read_bundle_data(namespace, ca_bundle_name(MDB_RESOURCE, CLIENT)) == state["client_bundle"]
    ), "pruning the server CA must not change the client bundle"

    wait_for_automation_config_ca_files(mongodb_multi, pruned["latest_hash"])
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)
    assert _pods(namespace, member_cluster_clients) == state["pods"], "pruning must not recreate the pods"


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_label_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    # The operator pod joins the Istio mesh, so ServiceEntries can later cut it off from one member cluster.
    api = kubernetes.client.CoreV1Api(api_client=central_cluster_client)
    ns = api.read_namespace(name=namespace)
    ns.metadata.labels.update({"istio-injection": "enabled"})
    api.replace_namespace(name=namespace, body=ns)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_create_service_entries(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    allow_operator_to_reach(namespace, central_cluster_client, get_member_cluster_names())


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_mongodb_multi_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_bundles_replicated_to_every_member_cluster(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    for category in (SERVER, CLIENT):
        wait_for_bundle_replicated(namespace, ca_bundle_name(MDB_RESOURCE, category), member_cluster_clients)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_certificate_renewals_reported(mongodb_multi: MongoDBMulti, namespace: str):
    assert_certificate_renewals_reported(mongodb_multi, namespace, [MEMBER_LEAF], CLIENT_LEAVES)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_application_connects(mongodb_multi: MongoDBMulti, app_user: MongoDBUser, namespace: str):
    # Skipped locally: the runner is outside the clusters and cannot resolve the member services.
    assert_reachable_with_client_certificate(mongodb_multi, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_leaf_renewal_on_the_same_ca(
    mongodb_multi: MongoDBMulti, namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    """A renewed server leaf reaches every member cluster and the members roll onto it in place."""
    pods_before = _pods(namespace, member_cluster_clients)
    bundle_before = read_bundle_data(namespace, ca_bundle_name(MDB_RESOURCE, SERVER))
    server_ca = read_secret(namespace, ca_certificate_name(MDB_RESOURCE, SERVER))["tls.crt"]
    revision_before = read_certificate_status(namespace, MEMBER_LEAF).get("revision", 0)
    leaf_before = read_secret(namespace, MEMBER_LEAF)["tls.crt"]
    hash_before = pem_latest_hash(namespace, f"{MEMBER_LEAF}-pem", member_cluster_clients)

    trigger_certificate_renewal(namespace, MEMBER_LEAF)

    wait_for_certificate_revision_above(namespace, MEMBER_LEAF, revision_before)
    leaf = wait_for_leaf_signed_by(namespace, MEMBER_LEAF, leaf_before, server_ca, timeout=600)
    assert leaf != leaf_before
    wait_for_pem_secret_rolled_in_clusters(namespace, f"{MEMBER_LEAF}-pem", hash_before, member_cluster_clients)

    _wait_for_automation_config_to_name_current_files(namespace, member_cluster_clients)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)
    assert (
        read_bundle_data(namespace, ca_bundle_name(MDB_RESOURCE, SERVER)) == bundle_before
    ), "a leaf renewal must not change the bundle"
    assert _pods(namespace, member_cluster_clients) == pods_before, "a leaf renewal must not recreate the pods"


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_server_ca_rotation_replicates_the_widened_bundle(
    mongodb_multi: MongoDBMulti, namespace: str, member_cluster_clients: List[MultiClusterClient], rotation: dict
):
    """The widened bundle, previousHash included, reaches every member cluster and the members move onto it."""
    state = _rotate_server_ca(namespace, member_cluster_clients)
    rotation["first"] = state

    _assert_widened_bundle_replicated(namespace, state, member_cluster_clients)
    client_hash = state["client_bundle"]["latestHash"]
    wait_for_automation_config_ca_files(mongodb_multi, state["widened"]["latest_hash"], client_hash)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_server_leaf_reissued_on_the_new_ca(
    mongodb_multi: MongoDBMulti, namespace: str, member_cluster_clients: List[MultiClusterClient], rotation: dict
):
    _assert_rotation_completed(mongodb_multi, namespace, rotation["first"], member_cluster_clients)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_application_connects_after_rotation(mongodb_multi: MongoDBMulti, app_user: MongoDBUser, namespace: str):
    assert_reachable_with_client_certificate(mongodb_multi, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
def test_prune_previous_server_ca(
    mongodb_multi: MongoDBMulti, namespace: str, member_cluster_clients: List[MultiClusterClient], rotation: dict
):
    _prune_server_ca(mongodb_multi, namespace, rotation["first"], member_cluster_clients)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_application_connects_after_prune(mongodb_multi: MongoDBMulti, app_user: MongoDBUser, namespace: str):
    assert_reachable_with_client_certificate(mongodb_multi, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)


# A server CA rotation while one member cluster is unreachable. Skipped locally: a local operator is not in the
# Istio mesh, so the ServiceEntries cannot cut it off from a member cluster.


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_cut_the_operator_off_from_one_member_cluster(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    unreachable_cluster: MultiClusterClient,
    rotation: dict,
):
    # Recorded while every cluster is still reachable, so the end of the scenario can check that no pod in any
    # cluster, the unreachable one included, was recreated.
    rotation["pods_before_partition"] = _pods(namespace, member_cluster_clients)

    healthy = [c.cluster_name for c in member_cluster_clients if c.cluster_name != unreachable_cluster.cluster_name]
    allow_operator_to_reach(namespace, central_cluster_client, healthy)
    wait_for_failed_cluster(mongodb_multi, unreachable_cluster.cluster_name, failed=True)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_server_ca_rotation_while_a_member_cluster_is_unreachable(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    unreachable_cluster: MultiClusterClient,
    rotation: dict,
):
    """The operator keeps publishing the widened bundle to the member clusters it can reach.

    The unreachable cluster keeps the bundle it had: it only gets the widened one once it is back.
    """
    bundle_name = ca_bundle_name(MDB_RESOURCE, SERVER)
    stale = read_bundle_data(namespace, bundle_name, unreachable_cluster.api_client)

    state = _rotate_server_ca(namespace, member_cluster_clients)
    state["pods"] = rotation["pods_before_partition"]
    rotation["partition"] = state

    healthy = [c for c in member_cluster_clients if c.cluster_name != unreachable_cluster.cluster_name]
    _assert_widened_bundle_replicated(namespace, state, healthy)

    assert (
        read_bundle_data(namespace, bundle_name, unreachable_cluster.api_client) == stale
    ), "the operator cannot reach the cluster, so its bundle must be the one from before the rotation"
    assert stale.get("latestHash") != state["widened"]["latest_hash"]


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_member_cluster_reachable_again(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    unreachable_cluster: MultiClusterClient,
):
    allow_operator_to_reach(namespace, central_cluster_client, get_member_cluster_names())
    wait_for_failed_cluster(mongodb_multi, unreachable_cluster.cluster_name, failed=False)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_recovered_cluster_catches_up_with_the_rotation(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    rotation: dict,
):
    """The recovered cluster gets the widened bundle and the re-issued leaf, like the others."""
    state = rotation["partition"]
    _assert_widened_bundle_replicated(namespace, state, member_cluster_clients)
    _assert_rotation_completed(mongodb_multi, namespace, state, member_cluster_clients)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_prune_after_the_cluster_recovered(
    mongodb_multi: MongoDBMulti, namespace: str, member_cluster_clients: List[MultiClusterClient], rotation: dict
):
    _prune_server_ca(mongodb_multi, namespace, rotation["partition"], member_cluster_clients)


@mark.e2e_managed_certificate_multi_cluster_rs_rotation
@skip_if_local
def test_application_connects_after_the_cluster_recovered(
    mongodb_multi: MongoDBMulti, app_user: MongoDBUser, namespace: str
):
    assert_reachable_with_client_certificate(mongodb_multi, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)
