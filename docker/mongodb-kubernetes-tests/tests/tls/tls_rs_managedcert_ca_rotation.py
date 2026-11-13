"""Safe rotation of the operator's self-signed CAs for a replica set with operator-managed certificates.

Agents and members authenticate with x509, so both certificate categories are in use: the server CA signs
the member certificate, the client CA signs the clusterfile and agent certificates and the x509
certificate of the application user. After a CA rotation the operator widens the category's trust bundle
with the new CA, keeps the previous one, rolls the members onto the new bundle without recreating the
pods, then re-issues every leaf still signed by the previous root. The previous CA is only dropped on
request (the prune annotation) and never while a certificate signed by it is still in use.

The application user connects in the background for the whole module, so a rotation or a prune that
locks it out is caught even if it recovers before the next explicit check.
"""

import pytest
from kubetester import read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests import test_logger
from tests.conftest import local_operator
from tests.opsmanager.managedcert_helper import (
    begin_ca_rotation,
    pod_uids,
    wait_for_ca_rotated,
    wait_for_leaf_reissued_on_new_ca,
)
from tests.tls.managedcert_ca_rotation_helper import (
    CLIENT,
    MANAGED_CA_ROTATED_REASON,
    SERVER,
    USER_CERT_SECRET,
    USER_CERTIFICATE,
    ConnectivityMonitor,
    assert_not_pruned_ahead_of_leaves,
    assert_prune_annotation_kept,
    bundle_config_map_name,
    bundle_contains,
    cert_manager_controller_paused,
    certificates_ready_message,
    client_signed_secrets,
    fingerprints,
    member_cert_covers,
    previous_ca_not_pruned,
    previous_ca_retained,
    prune_annotation,
    read_bundle,
    read_leaf,
    restart_operator_pods,
    self_signed_ca_name,
    set_prune_annotation,
    signed_by,
    wait_for_bundle,
    wait_for_ca_file_paths,
    wait_for_certificates_ready_message,
    wait_for_issuing_reason,
    wait_for_leaves_signed_by,
    wait_for_pem_matches_leaf,
    wait_for_prune_annotation_removed,
)
from tests.tls.managedcert_helper import (
    assert_reachable_with_client_certificate,
    client_certificate_user,
    leaf_secret_names,
    managed_cert_replica_set,
    managed_certificate_operator,
    pem_secret_hashes,
    read_certificate_status,
    scale_and_assert_member_cert_issuance,
    trigger_certificate_renewal,
    wait_for_automation_config_to_use_leaf_secrets,
    wait_for_certificate_renewals,
    wait_for_certificates_ready_condition,
)

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE = "test-tls-rs-managedcert-ca-rotation"

# Restarting a member can fail the background probes while a new primary is elected. A trust problem
# (a pruned CA still in use, a bundle that misses the new CA) locks the user out for good, far longer.
MAX_OUTAGE_SECONDS = 300

# The deployment is expected to be reachable whenever connectivity is checked explicitly.
CONNECTIVITY_ATTEMPTS = 5

# How long an annotation the operator must ignore is watched.
IGNORED_ANNOTATION_SECONDS = 120

# Rotations roll every member, twice: onto the widened bundle, then onto the re-issued leaf.
ROLL_TIMEOUT = 1800

POD_PREFIX = f"{MDB_RESOURCE}-"


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str, custom_mdb_version: str) -> MongoDB:
    # No issuer for either category: the operator self-signs both the server and the client CAs, so it
    # owns their rotation and re-issues the leaves itself.
    return managed_cert_replica_set(MDB_RESOURCE, namespace, custom_mdb_version, {})


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


def _assert_connected(mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor):
    assert_reachable_with_client_certificate(mdb, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)
    connectivity.assert_no_outage(MAX_OUTAGE_SECONDS)


def _assert_pods_kept(namespace: str, before: dict):
    after = pod_uids(namespace, POD_PREFIX)
    for pod, uid in before.items():
        assert after.get(pod) == uid, f"{pod} must not be recreated by a CA rotation"


def _current_ca(namespace: str, category: str) -> str:
    return read_secret(namespace, self_signed_ca_name(MDB_RESOURCE, category))["tls.crt"]


def _member() -> str:
    return leaf_secret_names(MDB_RESOURCE)[0]


def _wait_for_member_on(mdb: MongoDB, namespace: str, leaf_before: str, pem_before: str, new_ca: str, old_cas: list):
    """Waits for the member certificate to be re-issued on new_ca and rolled to the members."""
    member = _member()
    leaf, _ = wait_for_leaf_reissued_on_new_ca(
        namespace, member, f"{member}-pem", leaf_before, pem_before, new_ca, timeout=ROLL_TIMEOUT
    )
    for old_ca in old_cas:
        assert not cert_signed_by_bundle(leaf, old_ca), "the re-issued member certificate must not use a previous CA"
    wait_for_pem_matches_leaf(namespace, member)
    wait_for_automation_config_to_use_leaf_secrets(mdb, namespace, timeout=ROLL_TIMEOUT)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_application_user_certificate_is_signed_by_the_client_ca(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    """x509 client certificates are required, so the client CA signs the application user's certificate.

    That makes the user certificate part of every client CA rotation below, like the clusterfile and
    agent certificates.
    """
    assert_reachable_with_client_certificate(mdb, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)

    app_user.load()
    assert app_user["status"]["clientCertificate"]["secretName"] == USER_CERT_SECRET
    assert app_user["status"]["clientCertificate"]["serverCAConfigMapName"] == bundle_config_map_name(
        MDB_RESOURCE, SERVER
    )
    user_crt = read_leaf(namespace, USER_CERT_SECRET)
    assert cert_signed_by_bundle(user_crt, _current_ca(namespace, CLIENT)), "the client CA must sign the user cert"
    assert not cert_signed_by_bundle(user_crt, _current_ca(namespace, SERVER))


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_certificates_ready_and_renewals_reported(mdb: MongoDB, namespace: str, connectivity: ConnectivityMonitor):
    """Steady state: one CA per bundle, the automation config names both bundles, renewals are reported."""
    condition = wait_for_certificates_ready_condition(mdb, "True", "AllCertificatesReady", timeout=600)
    assert "retained" not in condition.get("message", ""), "nothing is retained before any rotation"

    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    renewals = wait_for_certificate_renewals(mdb)
    assert renewals["server"]["name"] == member
    assert renewals["client"]["name"] in {clusterfile, agent}
    for category in (SERVER, CLIENT):
        assert renewals[category].get("notAfter"), f"the {category} renewal must carry the certificate expiry"
        assert renewals[category].get("renewalTime"), f"the {category} renewal must carry the next renewal time"

    server = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    assert server["certificates"] == fingerprints([_current_ca(namespace, SERVER)])
    assert client["certificates"] == fingerprints([_current_ca(namespace, CLIENT)])
    assert server["alias"] == server["bundle"], "ca-pem must hold the current server bundle"
    assert client["alias"] is None, "the client bundle has no fixed key"
    assert server["published_at"] and client["published_at"], "both bundles must carry the published-at annotation"
    wait_for_ca_file_paths(mdb, server["latest_hash"], client["latest_hash"])

    connectivity.assert_no_outage(MAX_OUTAGE_SECONDS)


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_server_ca_rotation(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """A server CA rotation widens the server bundle, rolls the members and moves the member certificate.

    The client category is not touched: its bundle, its certificates and the user certificate stay.
    """
    member = _member()
    before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    client_leaves_before = {secret: read_leaf(namespace, secret) for secret in client_signed_secrets(MDB_RESOURCE)}
    uids = pod_uids(namespace, POD_PREFIX)
    leaf_before = read_leaf(namespace, member)
    pem_before, _ = pem_secret_hashes(namespace, f"{member}-pem")
    revision_before = read_certificate_status(namespace, member).get("revision", 0)

    old_ca = begin_ca_rotation(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER))
    new_ca = wait_for_ca_rotated(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER), old_ca)

    widened = wait_for_bundle(
        namespace,
        MDB_RESOURCE,
        SERVER,
        [old_ca, new_ca],
        changed_from=before["latest_hash"],
        published_after=before["published_at"],
    )
    assert len(widened["certificates"]) == 2, "the widened bundle holds the previous and the new CA"
    assert widened["previous_hash"] == before["latest_hash"], "the previous bundle stays for members not rolled yet"
    wait_for_ca_file_paths(mdb, widened["latest_hash"], client_before["latest_hash"])
    wait_for_certificates_ready_message(mdb, previous_ca_retained(SERVER))

    _wait_for_member_on(mdb, namespace, leaf_before, pem_before, new_ca, [old_ca])
    assert read_certificate_status(namespace, member).get("revision", 0) > revision_before

    # Re-issuing the leaf does not change the trust: the previous CA stays until it is pruned.
    assert read_bundle(namespace, MDB_RESOURCE, SERVER)["certificates"] == widened["certificates"]
    assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]
    for secret, crt in client_leaves_before.items():
        assert read_leaf(namespace, secret) == crt, f"a server CA rotation must not re-issue {secret}"

    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)
    rotation["server_cas"] = [old_ca, new_ca]


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_second_server_ca_rotation_with_manual_leaf_renewal(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """Two rotations before a prune, the second one racing a manual renewal of the member certificate.

    The bundle keeps every CA it has not been asked to drop, so it holds three. The manual renewal can be
    signed by either root, depending on what cert-manager has cached; either way the member certificate
    has to end up on the newest root and the deployment Running.
    """
    member = _member()
    retained = rotation["server_cas"]
    before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    uids = pod_uids(namespace, POD_PREFIX)
    leaf_before = read_leaf(namespace, member)
    pem_before, _ = pem_secret_hashes(namespace, f"{member}-pem")

    old_ca = begin_ca_rotation(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER))
    assert fingerprints([old_ca]) == fingerprints([retained[-1]])
    new_ca = wait_for_ca_rotated(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER), old_ca)
    trigger_certificate_renewal(namespace, member)

    widened = wait_for_bundle(
        namespace,
        MDB_RESOURCE,
        SERVER,
        retained + [new_ca],
        changed_from=before["latest_hash"],
        published_after=before["published_at"],
    )
    assert len(widened["certificates"]) == 3, "both previous CAs stay until they are pruned"
    wait_for_ca_file_paths(mdb, server_hash=widened["latest_hash"])

    _wait_for_member_on(mdb, namespace, leaf_before, pem_before, new_ca, retained)
    wait_for_certificates_ready_condition(mdb, "True", "AllCertificatesReady", timeout=ROLL_TIMEOUT)

    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)
    rotation["server_cas"] = retained + [new_ca]


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_prune_server_drops_every_previous_server_ca(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """Pruning the server category drops both previous CAs at once and leaves the client bundle alone."""
    newest = rotation["server_cas"][-1]
    before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    uids = pod_uids(namespace, POD_PREFIX)

    set_prune_annotation(mdb, SERVER)
    wait_for_prune_annotation_removed(mdb)

    pruned = wait_for_bundle(namespace, MDB_RESOURCE, SERVER, [newest], changed_from=before["latest_hash"])
    assert len(pruned["certificates"]) == 1
    wait_for_ca_file_paths(mdb, pruned["latest_hash"], client_before["latest_hash"])
    wait_for_certificates_ready_message(mdb, previous_ca_retained(SERVER), present=False)
    assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]
    assert signed_by(namespace, _member(), newest)

    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)
    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)
    rotation["server_cas"] = [newest]


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_operator_restart_between_bundle_publication_and_reissue(
    mdb: MongoDB,
    app_user: MongoDBUser,
    namespace: str,
    operator: Operator,
    connectivity: ConnectivityMonitor,
    rotation: dict,
):
    """An operator restarted after publishing the widened bundle still re-issues the member certificate.

    The re-issue only happens after the members rolled onto the widened bundle, which takes minutes, so
    deleting the operator pod as soon as the bundle is published interrupts the reconcile in between. The
    new operator has to work out from the cluster alone that the member certificate is on the old root.
    """
    if local_operator():
        pytest.skip("the operator runs outside the cluster, so there is no operator pod to restart")

    member = _member()
    retained = rotation["server_cas"]
    before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    uids = pod_uids(namespace, POD_PREFIX)
    leaf_before = read_leaf(namespace, member)
    pem_before, _ = pem_secret_hashes(namespace, f"{member}-pem")

    old_ca = begin_ca_rotation(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER))
    new_ca = wait_for_ca_rotated(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER), old_ca)
    widened = wait_for_bundle(
        namespace, MDB_RESOURCE, SERVER, retained + [new_ca], changed_from=before["latest_hash"], timeout=900
    )
    if signed_by(namespace, member, new_ca):
        logger.warning("the member certificate was re-issued before the operator restart, the restart comes late")

    restart_operator_pods(operator)

    wait_for_ca_file_paths(mdb, server_hash=widened["latest_hash"])
    _wait_for_member_on(mdb, namespace, leaf_before, pem_before, new_ca, [old_ca])
    wait_for_certificates_ready_message(mdb, previous_ca_retained(SERVER))

    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)
    rotation["server_cas"] = retained + [new_ca]


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_scale_up_during_a_rotation(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """Members added while the server CA rotates get the widened bundle and a member cert on the new root."""
    member = _member()
    retained = rotation["server_cas"]
    before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    uids = pod_uids(namespace, POD_PREFIX)

    old_ca = begin_ca_rotation(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER))
    new_ca = wait_for_ca_rotated(namespace, self_signed_ca_name(MDB_RESOURCE, SERVER), old_ca)
    mdb.load()
    mdb["spec"]["members"] = 5
    mdb.update()

    widened = wait_for_bundle(
        namespace, MDB_RESOURCE, SERVER, retained + [new_ca], changed_from=before["latest_hash"], timeout=ROLL_TIMEOUT
    )
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)

    def member_cert_moved() -> bool:
        return member_cert_covers(namespace, MDB_RESOURCE, 5) and signed_by(namespace, member, new_ca)

    run_periodically(
        member_cert_moved, timeout=ROLL_TIMEOUT, sleep_time=5, msg="member cert for 5 members on the new CA"
    )
    wait_for_pem_matches_leaf(namespace, member)
    wait_for_automation_config_to_use_leaf_secrets(mdb, namespace, timeout=ROLL_TIMEOUT)
    wait_for_ca_file_paths(mdb, server_hash=widened["latest_hash"], timeout=ROLL_TIMEOUT)
    processes = mdb.get_automation_config_tester().automation_config["processes"]
    assert len(processes) == 5, "every new member must be in the automation config, with the widened bundle"
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)

    assert read_bundle(namespace, MDB_RESOURCE, SERVER)["certificates"] == widened["certificates"]
    after = pod_uids(namespace, POD_PREFIX)
    assert len(after) == 5
    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)
    rotation["server_cas"] = retained + [new_ca]


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_unknown_prune_value_is_ignored(mdb: MongoDB, namespace: str, connectivity: ConnectivityMonitor):
    """An annotation value that names no category prunes nothing, and the operator leaves it in place."""
    server_before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    assert len(server_before["certificates"]) > 1, "a previous server CA must be retained for this check"

    def nothing_pruned():
        assert read_bundle(namespace, MDB_RESOURCE, SERVER)["latest_hash"] == server_before["latest_hash"]
        assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]

    set_prune_annotation(mdb, "foo")
    assert_prune_annotation_kept(mdb, "foo", IGNORED_ANNOTATION_SECONDS, nothing_pruned)
    assert previous_ca_retained(SERVER) in certificates_ready_message(mdb)
    connectivity.assert_no_outage(MAX_OUTAGE_SECONDS)


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_prune_true_drops_the_retained_server_cas(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor, rotation: dict
):
    """The value true prunes every category; only the server category retains anything here."""
    newest = rotation["server_cas"][-1]
    before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)

    set_prune_annotation(mdb, "true")
    wait_for_prune_annotation_removed(mdb)

    pruned = wait_for_bundle(namespace, MDB_RESOURCE, SERVER, [newest], changed_from=before["latest_hash"])
    wait_for_ca_file_paths(mdb, pruned["latest_hash"], client_before["latest_hash"])
    wait_for_certificates_ready_message(mdb, previous_ca_retained(SERVER), present=False)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)
    _assert_connected(mdb, app_user, namespace, connectivity)
    rotation["server_cas"] = [newest]


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_scale_down(mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor):
    mdb.load()
    scale_and_assert_member_cert_issuance(mdb, MDB_RESOURCE, namespace, 3, reissued=False)
    _assert_connected(mdb, app_user, namespace, connectivity)


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_client_ca_rotation_prunes_only_after_every_client_certificate_moved(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor
):
    """A client CA rotation moves the clusterfile, agent and user certificates before the old CA goes.

    The prune is requested while the cert-manager controller is stopped, right after the widened bundle
    is published, so no certificate can have moved yet: the operator must refuse it, keep the
    annotation and mark every client certificate for re-issue. Once cert-manager runs again the
    certificates move, and the old client CA may only leave the bundle once none of them, the
    application user's included, is signed by it.
    """
    _, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    secrets = client_signed_secrets(MDB_RESOURCE)
    server_before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    uids = pod_uids(namespace, POD_PREFIX)
    pem_before = {secret: pem_secret_hashes(namespace, f"{secret}-pem")[0] for secret in (clusterfile, agent)}
    leaves_before = {secret: read_leaf(namespace, secret) for secret in (clusterfile, agent)}

    old_ca = begin_ca_rotation(namespace, self_signed_ca_name(MDB_RESOURCE, CLIENT))
    new_ca = wait_for_ca_rotated(namespace, self_signed_ca_name(MDB_RESOURCE, CLIENT), old_ca)
    widened = wait_for_bundle(
        namespace,
        MDB_RESOURCE,
        CLIENT,
        [old_ca, new_ca],
        changed_from=before["latest_hash"],
        published_after=before["published_at"],
    )
    assert widened["previous_hash"] == before["latest_hash"]

    refusal_checked = False
    with cert_manager_controller_paused():
        wait_for_ca_file_paths(mdb, server_before["latest_hash"], widened["latest_hash"])
        on_old_ca = [secret for secret in secrets if signed_by(namespace, secret, old_ca)]
        if on_old_ca != secrets:
            # Only possible when the members rolled faster than the controller was stopped.
            logger.warning(f"{set(secrets) - set(on_old_ca)} moved before the prune request, skipping the refusal")
        else:
            set_prune_annotation(mdb, CLIENT)
            # The reconcile that published the widened bundle waits for the members to roll before the
            # one triggered by the annotation can run.
            wait_for_certificates_ready_message(mdb, previous_ca_not_pruned(CLIENT), timeout=ROLL_TIMEOUT)
            assert prune_annotation(mdb) == CLIENT, "a refused prune keeps the annotation for a retry"
            assert bundle_contains(namespace, MDB_RESOURCE, CLIENT, old_ca), "a refused prune must keep the old CA"

            # After the members rolled onto the widened bundle the operator asks cert-manager to
            # re-issue every client certificate, the user's included. Nothing is issued while the
            # controller is stopped, so the request stays visible.
            for certificate in (clusterfile, agent, USER_CERTIFICATE):
                wait_for_issuing_reason(namespace, certificate, MANAGED_CA_ROTATED_REASON, timeout=ROLL_TIMEOUT)
            assert prune_annotation(mdb) == CLIENT
            assert bundle_contains(namespace, MDB_RESOURCE, CLIENT, old_ca)
            refusal_checked = True

    def never_pruned_ahead():
        assert_not_pruned_ahead_of_leaves(namespace, MDB_RESOURCE, CLIENT, old_ca, secrets)

    wait_for_leaves_signed_by(namespace, secrets, new_ca, timeout=ROLL_TIMEOUT, invariant=never_pruned_ahead)
    for secret in (clusterfile, agent):
        assert read_leaf(namespace, secret) != leaves_before[secret]
        wait_for_pem_matches_leaf(namespace, secret)
        assert pem_secret_hashes(namespace, f"{secret}-pem")[0] != pem_before[secret]
    assert not signed_by(namespace, USER_CERT_SECRET, old_ca), "the user certificate must leave the old client CA"
    wait_for_automation_config_to_use_leaf_secrets(mdb, namespace, timeout=ROLL_TIMEOUT)

    if not refusal_checked:
        set_prune_annotation(mdb, CLIENT)
    # The user certificate is owned by the MongoDBUser, so its re-issue does not wake the MongoDB
    # reconcile that retries the prune: allow one extra reconcile if nothing else triggers it.
    wait_for_prune_annotation_removed(mdb, timeout=ROLL_TIMEOUT)

    pruned = wait_for_bundle(namespace, MDB_RESOURCE, CLIENT, [new_ca], changed_from=widened["latest_hash"])
    wait_for_ca_file_paths(mdb, server_before["latest_hash"], pruned["latest_hash"], timeout=ROLL_TIMEOUT)
    wait_for_certificates_ready_message(mdb, previous_ca_retained(CLIENT), present=False)
    mdb.assert_reaches_phase(Phase.Running, timeout=ROLL_TIMEOUT)

    _assert_pods_kept(namespace, uids)
    _assert_connected(mdb, app_user, namespace, connectivity)


@pytest.mark.e2e_replica_set_tls_managedcert_ca_rotation
def test_prune_with_nothing_retained_removes_the_annotation(
    mdb: MongoDB, app_user: MongoDBUser, namespace: str, connectivity: ConnectivityMonitor
):
    """A prune request with nothing retained is fulfilled at once and changes no bundle."""
    server_before = read_bundle(namespace, MDB_RESOURCE, SERVER)
    client_before = read_bundle(namespace, MDB_RESOURCE, CLIENT)
    assert len(server_before["certificates"]) == 1 and len(client_before["certificates"]) == 1

    set_prune_annotation(mdb, SERVER)
    wait_for_prune_annotation_removed(mdb, timeout=600)

    assert read_bundle(namespace, MDB_RESOURCE, SERVER)["latest_hash"] == server_before["latest_hash"]
    assert read_bundle(namespace, MDB_RESOURCE, CLIENT)["latest_hash"] == client_before["latest_hash"]
    wait_for_certificates_ready_condition(mdb, "True", "AllCertificatesReady", timeout=600)
    assert "retained" not in certificates_ready_message(mdb)
    _assert_connected(mdb, app_user, namespace, connectivity)
