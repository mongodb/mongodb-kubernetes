"""Leaf renewal and self-signed CA rotation for a sharded cluster with operator-managed certificates.

The sharded cluster uses the operator's self-signed CAs for both categories, x509 for the agents and the
internal cluster authentication, and an application connects through mongos with an operator-issued x509
client certificate. Every component (mongos, config servers, each shard) has its own server and clusterfile
certificate, but there is one trust bundle per category for the whole resource.

The module first renews a shard and the mongos certificate on the same CA, then rotates the server CA and the
client CA one after the other: each rotation must widen the one shared bundle, move every component onto it
and re-issue every leaf of that category on the new root, without recreating a pod or losing the application.
Pruning both retained CAs at the end narrows the bundles to the new roots.
"""

import pytest
from kubetester import read_secret, try_load
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.managedcert_rotation_mc_helper import (
    CA_BUNDLE_KEY,
    CLIENT,
    SERVER,
    assert_certificate_renewals_reported,
    assert_single_ca_bundle_per_category,
    bundle_snapshot,
    ca_bundle_name,
    ca_certificate_name,
    leaf_certificates,
    user_certificate,
    wait_for_automation_config_ca_files,
    wait_for_automation_config_names_files,
    wait_for_leaf_signed_by,
    wait_for_prune_annotation_removed,
)
from tests.opsmanager.managedcert_helper import (
    begin_ca_rotation,
    bundle_fingerprints,
    pod_uids,
    wait_for_bundle_pruned,
    wait_for_bundle_rotation,
    wait_for_ca_rotated,
    wait_for_leaf_reissued_on_new_ca,
)
from tests.tls.managedcert_helper import (
    assert_reachable_with_client_certificate,
    client_certificate_user,
    managed_certificate_operator,
    pem_secret_hashes,
    read_certificate_status,
    request_prune_previous_ca,
    trigger_certificate_renewal,
    wait_for_certificate_issuance_after,
    wait_for_certificate_issuance_settled,
)

MDB_RESOURCE = "sc-managedcert-rotation"
SHARD_COUNT = 2

# Every component runs in its own StatefulSet, named after the component, with its own certificates.
COMPONENTS = [f"{MDB_RESOURCE}-mongos", f"{MDB_RESOURCE}-config"] + [
    f"{MDB_RESOURCE}-{shard}" for shard in range(SHARD_COUNT)
]
SERVER_LEAVES = [f"{component}-managed-cert" for component in COMPONENTS]
CLUSTERFILE_LEAVES = [f"{component}-managed-clusterfile" for component in COMPONENTS]
AGENT_LEAF = f"{MDB_RESOURCE}-managed-agent-certs"
CLIENT_LEAVES = CLUSTERFILE_LEAVES + [AGENT_LEAF]

# The deployment is expected to be reachable whenever connectivity is checked, so a few attempts are enough.
CONNECTIVITY_ATTEMPTS = 5


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    # x509 for the agents and the internal cluster authentication, so both categories have leaves.
    resource = MongoDB.from_yaml(load_fixture("test-tls-sc-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["security"]["managedCertificate"] = {}
    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def app_user(sc: MongoDB, namespace: str) -> MongoDBUser:
    return client_certificate_user(sc, namespace)


@pytest.fixture(scope="module")
def rotation() -> dict:
    """The state each step of the rotation hands to the next one."""
    return {}


def _assert_reachable(sc: MongoDB, app_user: MongoDBUser, namespace: str):
    assert_reachable_with_client_certificate(sc, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)


def _pods(namespace: str) -> dict[str, str]:
    return pod_uids(namespace, f"{MDB_RESOURCE}-")


def _wait_for_automation_config_to_name_current_files(namespace: str):
    agent_hash, _ = pem_secret_hashes(namespace, f"{AGENT_LEAF}-pem")
    member_hashes = {c: pem_secret_hashes(namespace, f"{c}-managed-cert-pem")[0] for c in COMPONENTS}
    clusterfile_hashes = {c: pem_secret_hashes(namespace, f"{c}-managed-clusterfile-pem")[0] for c in COMPONENTS}
    wait_for_automation_config_names_files(agent_hash, member_hashes, clusterfile_hashes)


def _current_ca(namespace: str, category: str) -> str:
    return read_secret(namespace, ca_certificate_name(MDB_RESOURCE, category))["tls.crt"]


def _rotate_ca(namespace: str, category: str, app_user: MongoDBUser, rotation: dict) -> dict:
    """Rotates a category's self-signed CA and returns the widened bundle snapshot.

    The leaves of the category, their -pem hashes, and the leaves of the other category are recorded first, so
    the next step can tell what was re-issued.
    """
    leaves = SERVER_LEAVES if category == SERVER else CLIENT_LEAVES
    other_leaves = CLIENT_LEAVES if category == SERVER else SERVER_LEAVES
    rotation[category] = {
        "leaves": leaf_certificates(namespace, leaves),
        "pem_hashes": {leaf: pem_secret_hashes(namespace, f"{leaf}-pem")[0] for leaf in leaves},
        "revisions": {leaf: read_certificate_status(namespace, leaf).get("revision", 0) for leaf in leaves},
        "other_leaves": leaf_certificates(namespace, other_leaves),
        "user_cert": user_certificate(app_user, namespace)[1],
    }

    bundle_before = bundle_snapshot(namespace, MDB_RESOURCE, category)
    assert bundle_before["latest_hash"], f"the {category} bundle must have a latestHash"
    assert bundle_before["published_at"], f"the {category} bundle must have a published-at annotation"
    rotation[category]["bundle_before"] = bundle_before

    ca_name = ca_certificate_name(MDB_RESOURCE, category)
    old_ca = begin_ca_rotation(namespace, ca_name)
    new_ca = wait_for_ca_rotated(namespace, ca_name, old_ca)
    rotation[category]["old_ca"] = old_ca
    rotation[category]["new_ca"] = new_ca

    widened = wait_for_bundle_rotation(
        namespace,
        ca_bundle_name(MDB_RESOURCE, category),
        CA_BUNDLE_KEY,
        bundle_before["latest_hash"],
        bundle_before["published_at"],
    )
    assert set(widened["certificates"]) == set(bundle_fingerprints(old_ca)) | set(
        bundle_fingerprints(new_ca)
    ), f"the widened {category} bundle must hold the previous and the new CA"
    assert len(widened["certificates"]) == 2, f"the {category} bundle must hold exactly 2 CAs before pruning"
    rotation[category]["widened"] = widened
    return widened


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_sharded_cluster_running(sc: MongoDB):
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_application_connects_through_mongos(sc: MongoDB, app_user: MongoDBUser, namespace: str):
    _assert_reachable(sc, app_user, namespace)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_certificate_renewals_reported(sc: MongoDB, namespace: str):
    """The renewal status covers the certificates of every component, per category."""
    assert_certificate_renewals_reported(sc, namespace, SERVER_LEAVES, CLIENT_LEAVES)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_leaf_renewal_on_the_same_ca(sc: MongoDB, app_user: MongoDBUser, namespace: str):
    """A shard and the mongos certificate are renewed on the same CA, and their members roll in place.

    Only the renewed certificates change: the other components keep theirs, and the bundle stays the same
    because the CA did not change.
    """
    renewed = [f"{MDB_RESOURCE}-0-managed-cert", f"{MDB_RESOURCE}-mongos-managed-cert"]
    untouched = [leaf for leaf in SERVER_LEAVES if leaf not in renewed]

    pods_before = _pods(namespace)
    assert pods_before, "the sharded cluster pods must exist before the renewal"
    bundle_before = bundle_snapshot(namespace, MDB_RESOURCE, SERVER)
    server_ca = _current_ca(namespace, SERVER)
    untouched_revisions = {leaf: read_certificate_status(namespace, leaf).get("revision", 0) for leaf in untouched}

    before = {}
    for leaf in renewed:
        revision, latest = wait_for_certificate_issuance_settled(namespace, leaf, f"{leaf}-pem")
        before[leaf] = (revision, latest, read_secret(namespace, leaf)["tls.crt"])

    for leaf in renewed:
        trigger_certificate_renewal(namespace, leaf)

    for leaf in renewed:
        revision, latest, crt = before[leaf]
        new_revision, new_latest, previous = wait_for_certificate_issuance_after(
            namespace, leaf, f"{leaf}-pem", revision, latest, timeout=600
        )
        assert new_revision > revision
        assert previous == latest, f"{leaf}-pem must keep the certificate it replaced"
        new_crt = read_secret(namespace, leaf)["tls.crt"]
        assert new_crt != crt, f"{leaf} must hold a newly issued certificate"
        assert cert_signed_by_bundle(new_crt, server_ca), f"{leaf} must be renewed on the same server CA"

    for leaf, revision in untouched_revisions.items():
        assert (
            read_certificate_status(namespace, leaf).get("revision", 0) == revision
        ), f"{leaf} must not be issued again"

    _wait_for_automation_config_to_name_current_files(namespace)
    sc.assert_reaches_phase(Phase.Running, timeout=900)

    bundle_after = bundle_snapshot(namespace, MDB_RESOURCE, SERVER)
    assert bundle_after["latest_hash"] == bundle_before["latest_hash"], "a leaf renewal must not change the bundle"
    assert _pods(namespace) == pods_before, "a leaf renewal must not recreate the pods"
    _assert_reachable(sc, app_user, namespace)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_server_ca_rotation_widens_the_shared_bundle(
    sc: MongoDB, app_user: MongoDBUser, namespace: str, rotation: dict
):
    """Rotating the server CA widens the one server bundle and every component moves onto it.

    The previous CA is kept, so the components keep trusting the leaves still signed by it, and the
    application keeps connecting with the bundle it reads.
    """
    rotation["pods"] = _pods(namespace)
    client_bundle_before = bundle_snapshot(namespace, MDB_RESOURCE, CLIENT)

    widened = _rotate_ca(namespace, SERVER, app_user, rotation)

    assert_single_ca_bundle_per_category(namespace, MDB_RESOURCE)
    assert (
        bundle_snapshot(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_bundle_before["latest_hash"]
    ), "a server CA rotation must not change the client bundle"

    wait_for_automation_config_ca_files(sc, widened["latest_hash"], client_bundle_before["latest_hash"])
    _assert_reachable(sc, app_user, namespace)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_every_component_server_leaf_reissued_on_the_new_ca(
    sc: MongoDB, app_user: MongoDBUser, namespace: str, rotation: dict
):
    """The operator moves the server leaf of every component onto the new root.

    The client leaves (clusterfile, agent and the application's) are signed by the client CA and stay as they
    are. The bundle keeps both CAs until a prune is requested.
    """
    state = rotation[SERVER]
    for leaf in SERVER_LEAVES:
        new_leaf, _ = wait_for_leaf_reissued_on_new_ca(
            namespace,
            leaf,
            f"{leaf}-pem",
            state["leaves"][leaf],
            state["pem_hashes"][leaf],
            state["new_ca"],
        )
        assert not cert_signed_by_bundle(new_leaf, state["old_ca"]), f"{leaf} must not be signed by the old CA"
        assert read_certificate_status(namespace, leaf).get("revision", 0) > state["revisions"][leaf]

    assert (
        leaf_certificates(namespace, CLIENT_LEAVES) == state["other_leaves"]
    ), "a server CA rotation must not re-issue the client leaves"
    assert (
        user_certificate(app_user, namespace)[1] == state["user_cert"]
    ), "the application certificate is client-signed, a server CA rotation must not re-issue it"

    _wait_for_automation_config_to_name_current_files(namespace)
    sc.assert_reaches_phase(Phase.Running, timeout=1200)
    assert _pods(namespace) == rotation["pods"], "a CA rotation must not recreate the pods"
    assert (
        len(bundle_snapshot(namespace, MDB_RESOURCE, SERVER)["certificates"]) == 2
    ), "the previous server CA is kept until it is pruned"
    _assert_reachable(sc, app_user, namespace)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_client_ca_rotation_widens_the_shared_bundle(
    sc: MongoDB, app_user: MongoDBUser, namespace: str, rotation: dict
):
    """Rotating the client CA widens the one client bundle, which every component's clusterCAFile names."""
    server_bundle_before = bundle_snapshot(namespace, MDB_RESOURCE, SERVER)

    widened = _rotate_ca(namespace, CLIENT, app_user, rotation)

    assert_single_ca_bundle_per_category(namespace, MDB_RESOURCE)
    assert (
        bundle_snapshot(namespace, MDB_RESOURCE, SERVER)["latest_hash"] == server_bundle_before["latest_hash"]
    ), "a client CA rotation must not change the server bundle"

    wait_for_automation_config_ca_files(sc, server_bundle_before["latest_hash"], widened["latest_hash"])
    _assert_reachable(sc, app_user, namespace)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_every_client_leaf_reissued_on_the_new_ca(sc: MongoDB, app_user: MongoDBUser, namespace: str, rotation: dict):
    """The operator moves the clusterfile of every component, the agent and the application certificate onto
    the new client root, and the server leaves stay as they are."""
    state = rotation[CLIENT]
    for leaf in CLIENT_LEAVES:
        new_leaf, _ = wait_for_leaf_reissued_on_new_ca(
            namespace,
            leaf,
            f"{leaf}-pem",
            state["leaves"][leaf],
            state["pem_hashes"][leaf],
            state["new_ca"],
        )
        assert not cert_signed_by_bundle(new_leaf, state["old_ca"]), f"{leaf} must not be signed by the old CA"
        assert read_certificate_status(namespace, leaf).get("revision", 0) > state["revisions"][leaf]

    user_secret, _ = user_certificate(app_user, namespace)
    user_cert = wait_for_leaf_signed_by(namespace, user_secret, state["user_cert"], state["new_ca"])
    assert not cert_signed_by_bundle(user_cert, state["old_ca"]), "the application certificate must move off the old CA"

    assert (
        leaf_certificates(namespace, SERVER_LEAVES) == state["other_leaves"]
    ), "a client CA rotation must not re-issue the server leaves"

    _wait_for_automation_config_to_name_current_files(namespace)
    sc.assert_reaches_phase(Phase.Running, timeout=1200)
    assert _pods(namespace) == rotation["pods"], "a CA rotation must not recreate the pods"
    assert (
        len(bundle_snapshot(namespace, MDB_RESOURCE, CLIENT)["certificates"]) == 2
    ), "the previous client CA is kept until it is pruned"
    _assert_reachable(sc, app_user, namespace)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_rotation
def test_prune_both_previous_cas(sc: MongoDB, app_user: MongoDBUser, namespace: str, rotation: dict):
    """Pruning both categories narrows each bundle to its new CA, and the operator removes the annotation."""
    request_prune_previous_ca(sc, "true")
    wait_for_prune_annotation_removed(sc)

    pruned = {}
    for category in (SERVER, CLIENT):
        new_ca = rotation[category]["new_ca"]
        pruned[category] = wait_for_bundle_pruned(
            namespace, ca_bundle_name(MDB_RESOURCE, category), CA_BUNDLE_KEY, new_ca
        )
        assert set(pruned[category]["certificates"]) == set(bundle_fingerprints(new_ca))

    wait_for_automation_config_ca_files(sc, pruned[SERVER]["latest_hash"], pruned[CLIENT]["latest_hash"])
    sc.assert_reaches_phase(Phase.Running, timeout=1200)
    assert _pods(namespace) == rotation["pods"], "pruning must not recreate the pods"
    _assert_reachable(sc, app_user, namespace)
