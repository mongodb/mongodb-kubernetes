"""Rotation of a customer CA that signs both certificate categories of a managed-certificate replica set.

The customer owns the CA: the operator does not rotate it and does not re-issue certificates when it
changes. The rotation is the customer's procedure, and this module walks through it:

1. The issuer's CA key pair is replaced, keeping the Issuer. Nothing changes in the deployment: the
   operator reads the CA from the issued certificates, which are not re-issued.
2. The new CA is trusted through additionalTrustedCAs: both bundles widen and the members roll onto
   them without being recreated.
3. The certificates are renewed by hand, one group at a time. A prune requested while any of them, the
   application user's certificate included, is still signed by the old CA is refused and kept; once the
   last one moved the prune completes and the application user still connects.

It also checks that editing a referenced additionalTrustedCAs ConfigMap rebuilds the bundle and rolls
the members.
"""

import copy

import pytest
from kubetester import create_or_update_configmap
from kubetester.crypto import cert_signed_by_bundle
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.opsmanager.managedcert_helper import customer_ca_bundle, pod_uids
from tests.tls.managedcert_ca_rotation_helper import (
    CLIENT,
    SERVER,
    USER_CERT_SECRET,
    USER_CERTIFICATE,
    ConnectivityMonitor,
    assert_during,
    assert_not_pruned_ahead_of_leaves,
    assert_prune_annotation_kept,
    bundle_contains,
    client_signed_secrets,
    fingerprints,
    generate_ca,
    previous_ca_not_pruned,
    previous_ca_retained,
    prune_annotation,
    read_bundle,
    read_leaf,
    renew_until_signed_by,
    replace_issuer_ca,
    set_prune_annotation,
    signed_by,
    wait_for_bundle,
    wait_for_ca_file_paths,
    wait_for_certificates_ready_message,
    wait_for_pem_matches_leaf,
    wait_for_prune_annotation_removed,
)
from tests.tls.managedcert_helper import (
    CA_ISSUER_REF,
    assert_reachable_with_client_certificate,
    client_certificate_user,
    leaf_secret_names,
    managed_cert_replica_set,
    managed_certificate_operator,
    read_certificate_status,
    wait_for_automation_config_to_use_leaf_secrets,
    wait_for_certificates_ready_condition,
)

MDB_RESOURCE = "test-tls-rs-managedcert-issuer-ca-rotation"
POD_PREFIX = f"{MDB_RESOURCE}-"

# The ConfigMaps the customer uses to trust extra CAs, under the key the references name.
ROTATED_CA_CONFIG_MAP = "rotated-issuer-ca"
EXTRA_CA_CONFIG_MAP = "extra-trusted-ca"
CA_KEY = "ca.crt"

MAX_OUTAGE_SECONDS = 300
CONNECTIVITY_ATTEMPTS = 5
ROLL_TIMEOUT = 1800

# How long the test watches for an automatic re-issue that must not happen.
NO_REISSUE_SECONDS = 120


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str, custom_mdb_version: str) -> MongoDB:
    # The customer issuer signs both categories, so every certificate, the user's included, comes from
    # the CA the test rotates.
    return managed_cert_replica_set(
        MDB_RESOURCE,
        namespace,
        custom_mdb_version,
        {"server": copy.deepcopy(CA_ISSUER_REF), "client": copy.deepcopy(CA_ISSUER_REF)},
    )


@pytest.fixture(scope="module")
def app_user(mdb: MongoDB, namespace: str) -> MongoDBUser:
    return client_certificate_user(mdb, namespace)


@pytest.fixture(scope="module")
def connectivity(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    monitor = ConnectivityMonitor.for_user(mdb, app_user, namespace).start()
    yield monitor
    monitor.stop()


@pytest.fixture(scope="module")
def rotation() -> dict:
    """The CAs each step hands to the next one."""
    return {}


def _certificates() -> dict:
    """Every certificate the customer issuer signs, mapped to its secret."""
    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    return {member: member, clusterfile: clusterfile, agent: agent, USER_CERTIFICATE: USER_CERT_SECRET}


def _revisions(namespace: str) -> dict:
    return {name: read_certificate_status(namespace, name).get("revision", 0) for name in _certificates()}


def _leaves(namespace: str) -> dict:
    return {secret: read_leaf(namespace, secret) for secret in _certificates().values()}


def _assert_connected(mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor):
    assert_reachable_with_client_certificate(mdb, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)
    connectivity.assert_no_outage(MAX_OUTAGE_SECONDS)


def _assert_pods_kept(namespace: str, before: dict):
    after = pod_uids(namespace, POD_PREFIX)
    for pod, uid in before.items():
        assert after.get(pod) == uid, f"{pod} must not be recreated by a CA change"


def _ca_ref(name: str) -> dict:
    return {"kind": "ConfigMap", "name": name, "key": CA_KEY}


def _renew(mdb: MongoDB, namespace: str, certificates: list, ca_pem: str):
    """Renews the certificates by hand until the new CA signs them, then waits for the members to roll."""
    for certificate in certificates:
        renew_until_signed_by(namespace, certificate, _certificates()[certificate], ca_pem)
    for certificate in certificates:
        if certificate != USER_CERTIFICATE:
            wait_for_pem_matches_leaf(namespace, certificate)
    wait_for_automation_config_to_use_leaf_secrets(mdb, namespace, timeout=ROLL_TIMEOUT)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_every_certificate_is_signed_by_the_customer_ca(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, issuer: str, connectivity: ConnectivityMonitor, rotation: dict
):
    old_ca = customer_ca_bundle(namespace, issuer)
    for secret, leaf in _leaves(namespace).items():
        assert cert_signed_by_bundle(leaf, old_ca), f"the customer CA must sign {secret}"
    for category in (SERVER, CLIENT):
        assert read_bundle(namespace, MDB_RESOURCE, category)["certificates"] == fingerprints([old_ca])

    wait_for_certificates_ready_condition(mdb, "True", "AllCertificatesReady", timeout=600)
    _assert_connected(mdb, app_user, namespace, connectivity)
    rotation["old_ca"] = old_ca


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_replacing_the_issuer_ca_reissues_nothing(namespace: str, issuer: str, rotation: dict):
    """The operator does not move certificates of a customer CA; the customer decides when to renew."""
    revisions = _revisions(namespace)
    leaves = _leaves(namespace)
    server_before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)

    new_ca, new_key = generate_ca("e2e-rotated-customer-ca")
    replaced = replace_issuer_ca(namespace, issuer, new_ca, new_key)
    assert fingerprints([replaced]) == fingerprints([rotation["old_ca"]])

    def nothing_reissued():
        assert _revisions(namespace) == revisions, "no certificate may be re-issued for a customer CA"
        assert _leaves(namespace) == leaves
        assert read_bundle(namespace, MDB_RESOURCE, SERVER)["latest_hash"] == server_before["latest_hash"]
        assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]

    assert_during(NO_REISSUE_SECONDS, nothing_reissued)
    rotation["new_ca"] = new_ca


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_trusting_the_new_ca_widens_both_bundles(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """Trusting the new CA before any certificate moves widens both bundles and rolls the members."""
    old_ca, new_ca = rotation["old_ca"], rotation["new_ca"]
    revisions = _revisions(namespace)
    leaves = _leaves(namespace)
    server_before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    uids = pod_uids(namespace, POD_PREFIX)

    create_or_update_configmap(namespace, ROTATED_CA_CONFIG_MAP, {CA_KEY: new_ca})
    mdb.load()
    managed_certificate = mdb["spec"]["security"]["managedCertificate"]
    managed_certificate["server"]["additionalTrustedCAs"] = [_ca_ref(ROTATED_CA_CONFIG_MAP)]
    managed_certificate["client"]["additionalTrustedCAs"] = [_ca_ref(ROTATED_CA_CONFIG_MAP)]
    mdb.update()

    server = wait_for_bundle(
        namespace,
        MDB_RESOURCE,
        SERVER,
        [old_ca, new_ca],
        changed_from=server_before["latest_hash"],
        published_after=server_before["published_at"],
    )
    client = wait_for_bundle(
        namespace,
        MDB_RESOURCE,
        CLIENT,
        [old_ca, new_ca],
        changed_from=client_before["latest_hash"],
        published_after=client_before["published_at"],
    )
    assert server["previous_hash"] == server_before["latest_hash"]
    assert client["previous_hash"] == client_before["latest_hash"]
    wait_for_ca_file_paths(mdb, server["latest_hash"], client["latest_hash"], timeout=ROLL_TIMEOUT)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)

    assert _revisions(namespace) == revisions, "widening the trust must not re-issue any certificate"
    assert _leaves(namespace) == leaves
    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_renewed_member_and_clusterfile_certificates_retain_the_old_ca(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """Once the issued certificates name the new CA, the old one becomes a retained previous CA.

    The operator reads a customer issuer's CA from the issued member (server) and clusterfile (client)
    certificates, so renewing those two makes the new CA current; the bundles keep the same CAs.
    """
    old_ca, new_ca = rotation["old_ca"], rotation["new_ca"]
    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    server_before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    uids = pod_uids(namespace, POD_PREFIX)

    _renew(mdb, namespace, [member, clusterfile], new_ca)

    wait_for_certificates_ready_message(mdb, previous_ca_retained(SERVER))
    wait_for_certificates_ready_message(mdb, previous_ca_retained(CLIENT))
    assert read_bundle(namespace, MDB_RESOURCE, SERVER)["latest_hash"] == server_before["latest_hash"]
    assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]
    assert signed_by(namespace, agent, old_ca), "the agent certificate is only renewed later"
    assert signed_by(namespace, USER_CERT_SECRET, old_ca), "the user certificate is only renewed later"

    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_prune_is_refused_while_client_certificates_use_the_old_ca(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """The agent and user certificates still hold the old client CA, so its prune is refused.

    No server certificate uses the old CA any more, so the server part of the request is applied: the
    refusal is per category.
    """
    old_ca, new_ca = rotation["old_ca"], rotation["new_ca"]
    server_before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)

    set_prune_annotation(mdb, "true")
    wait_for_certificates_ready_message(mdb, previous_ca_not_pruned(CLIENT))
    assert prune_annotation(mdb) == "true", "a refused prune keeps the annotation for a retry"

    server = wait_for_bundle(namespace, MDB_RESOURCE, SERVER, [new_ca], changed_from=server_before["latest_hash"])
    wait_for_ca_file_paths(mdb, server["latest_hash"], client_before["latest_hash"], timeout=ROLL_TIMEOUT)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)

    def client_ca_kept():
        assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]

    assert_prune_annotation_kept(mdb, "true", 60, client_ca_kept)
    _assert_connected(mdb, app_user, namespace, connectivity)


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_prune_stays_refused_while_only_the_user_certificate_uses_the_old_ca(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """The application user's certificate alone must hold the old client CA in the bundle.

    Dropping it would lock the application out while every operator certificate is fine.
    """
    old_ca, new_ca = rotation["old_ca"], rotation["new_ca"]
    _, _, agent = leaf_secret_names(MDB_RESOURCE)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)

    # Renewing the agent certificate makes the operator reconcile and evaluate the prune again.
    _renew(mdb, namespace, [agent], new_ca)
    assert signed_by(namespace, USER_CERT_SECRET, old_ca)

    def client_ca_kept():
        assert bundle_contains(namespace, MDB_RESOURCE, CLIENT, old_ca), "the user certificate still needs the old CA"
        assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]

    wait_for_certificates_ready_message(mdb, previous_ca_not_pruned(CLIENT))
    assert_prune_annotation_kept(mdb, "true", 90, client_ca_kept)
    _assert_connected(mdb, app_user, namespace, connectivity)


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_prune_completes_once_the_user_certificate_moved(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    old_ca, new_ca = rotation["old_ca"], rotation["new_ca"]
    server = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    uids = pod_uids(namespace, POD_PREFIX)

    renew_until_signed_by(namespace, USER_CERTIFICATE, USER_CERT_SECRET, new_ca)
    assert_not_pruned_ahead_of_leaves(namespace, MDB_RESOURCE, CLIENT, old_ca, client_signed_secrets(MDB_RESOURCE))

    # The user certificate is owned by the MongoDBUser, so its renewal does not wake the MongoDB
    # reconcile that retries the prune: allow one extra reconcile if nothing else triggers it.
    wait_for_prune_annotation_removed(mdb, timeout=ROLL_TIMEOUT)
    client = wait_for_bundle(namespace, MDB_RESOURCE, CLIENT, [new_ca], changed_from=client_before["latest_hash"])
    wait_for_ca_file_paths(mdb, server["latest_hash"], client["latest_hash"], timeout=ROLL_TIMEOUT)
    wait_for_certificates_ready_message(mdb, previous_ca_retained(CLIENT), present=False)
    wait_for_certificates_ready_message(mdb, previous_ca_not_pruned(CLIENT), present=False)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)

    for secret, leaf in _leaves(namespace).items():
        assert cert_signed_by_bundle(leaf, new_ca), f"{secret} must be signed by the new CA"
    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)


@pytest.mark.e2e_replica_set_tls_managedcert_issuer_ca_rotation
def test_editing_an_additional_trusted_ca_rolls_the_members(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """Editing a referenced additionalTrustedCAs ConfigMap republishes the bundle and rolls the members.

    The CA that left the ConfigMap is kept like any previous CA until a prune drops it.
    """
    new_ca = rotation["new_ca"]
    client_hash = read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"]
    before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    uids = pod_uids(namespace, POD_PREFIX)

    first_extra, _ = generate_ca("e2e-extra-trusted-ca-1")
    create_or_update_configmap(namespace, EXTRA_CA_CONFIG_MAP, {CA_KEY: first_extra})
    mdb.load()
    mdb["spec"]["security"]["managedCertificate"]["server"]["additionalTrustedCAs"] = [
        _ca_ref(ROTATED_CA_CONFIG_MAP),
        _ca_ref(EXTRA_CA_CONFIG_MAP),
    ]
    mdb.update()

    added = wait_for_bundle(namespace, MDB_RESOURCE, SERVER, [new_ca, first_extra], changed_from=before["latest_hash"])
    wait_for_ca_file_paths(mdb, added["latest_hash"], client_hash, timeout=ROLL_TIMEOUT)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)

    second_extra, _ = generate_ca("e2e-extra-trusted-ca-2")
    create_or_update_configmap(namespace, EXTRA_CA_CONFIG_MAP, {CA_KEY: second_extra})

    edited = wait_for_bundle(
        namespace,
        MDB_RESOURCE,
        SERVER,
        [new_ca, first_extra, second_extra],
        changed_from=added["latest_hash"],
        published_after=added["published_at"],
    )
    assert edited["previous_hash"] == added["latest_hash"]
    wait_for_ca_file_paths(mdb, edited["latest_hash"], client_hash, timeout=ROLL_TIMEOUT)
    wait_for_certificates_ready_message(mdb, previous_ca_retained(SERVER))
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)
    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)

    set_prune_annotation(mdb, SERVER)
    wait_for_prune_annotation_removed(mdb)
    pruned = wait_for_bundle(
        namespace, MDB_RESOURCE, SERVER, [new_ca, second_extra], changed_from=edited["latest_hash"]
    )
    wait_for_ca_file_paths(mdb, pruned["latest_hash"], client_hash, timeout=ROLL_TIMEOUT)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)
    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)
