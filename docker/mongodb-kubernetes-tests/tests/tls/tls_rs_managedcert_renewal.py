"""Leaf certificate renewal for operator-managed certificates.

The resource uses one user-provided CA issuer for both certificate categories, so a renewal only
depends on cert-manager and the operator: shortening a certificate's lifetime makes cert-manager
renew it on its own, and the operator has to roll the members onto the new files without recreating
the pods. The module also checks that a broken issuer is reported as a certificate-renewal failure
without taking the deployment down, because the still-valid certificate keeps serving traffic.
"""

from datetime import datetime

import pytest
from kubernetes import client
from kubetester import list_matching_pods, read_secret
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    CA_ISSUER_REF,
    assert_reachable_with_client_certificate,
    break_issuer_ca,
    client_certificate_user,
    leaf_secret_names,
    managed_cert_replica_set,
    managed_certificate_operator,
    member_cert_issuance,
    pem_secret_hashes,
    read_certificate_renewals,
    read_certificate_status,
    restore_issuer_ca,
    retry_issuance_until_success,
    trigger_certificate_renewal,
    wait_for_automation_config_to_use_leaf_secrets,
    wait_for_automation_config_version_above,
    wait_for_certificate_failure,
    wait_for_certificate_issuance_after,
    wait_for_certificate_issuance_settled,
    wait_for_certificate_renewals,
    wait_for_certificate_spec_fields,
    wait_for_certificates_ready_condition,
)

MDB_RESOURCE = "test-tls-rs-managedcert-renewal"

# cert-manager requires duration >= 1h and 5m <= renewBefore < duration. With a 1h certificate and a
# 57m renewBefore cert-manager renews 3 minutes after issuance, soon enough for a bounded e2e test.
SHORT_DURATION = "1h"
SHORT_RENEW_BEFORE = "57m"

# The deployment is expected to be reachable whenever connectivity is checked, so a few attempts are
# enough; each failed one waits for the driver's server selection timeout.
CONNECTIVITY_ATTEMPTS = 5


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def mdb(namespace: str, issuer: str, custom_mdb_version: str) -> MongoDB:
    # Both categories use the user configured issuer, so every leaf is signed by the issuer the test
    # can break and restore, and the operator provisions no self-signed CA.
    return managed_cert_replica_set(
        MDB_RESOURCE, namespace, custom_mdb_version, {"server": CA_ISSUER_REF, "client": CA_ISSUER_REF}
    )


@pytest.fixture(scope="module")
def app_user(mdb: MongoDB, namespace: str) -> MongoDBUser:
    # x509 is the only authentication mode, so the operator makes mongod require a client certificate:
    # connectivity is checked the way an application connects, with an operator-issued x509 user.
    return client_certificate_user(mdb, namespace)


def _assert_reachable(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    assert_reachable_with_client_certificate(mdb, app_user, namespace, attempts=CONNECTIVITY_ATTEMPTS)


def _as_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def _server_renewal_time(mdb: MongoDB) -> datetime:
    renewals = read_certificate_renewals(mdb)
    assert renewals is not None, "status.certificateRenewals must be reported"
    return _as_time(renewals["server"]["renewalTime"])


def _pod_uids(mdb: MongoDB) -> dict[str, str]:
    pods = list_matching_pods(mdb.namespace, name_prefix=f"{mdb.name}-")
    return {pod.metadata.name: pod.metadata.uid for pod in pods}


def _printer_column(name: str):
    crd = client.ApiextensionsV1Api().read_custom_resource_definition("mongodb.mongodb.com")
    for version in crd.spec.versions:
        for column in version.additional_printer_columns or []:
            if column.name == name:
                return column
    raise AssertionError(f"CRD mongodb.mongodb.com has no {name} printer column")


def _certificate_condition(status: dict, condition_type: str) -> dict:
    for condition in status.get("conditions", []):
        if condition.get("type") == condition_type:
            return condition
    raise AssertionError(f"Certificate has no {condition_type} condition: {status}")


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_install_operator(operator: Operator):
    """The operator must be installed with cert-manager RBAC before it can manage any Certificate."""
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_replica_set_running(mdb: MongoDB):
    """Reaching Running proves the user issuer signed both categories and mTLS works end-to-end."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_application_connects_with_client_certificate(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    """An x509 application connects before any renewal, so later connectivity checks have a baseline."""
    _assert_reachable(mdb, app_user, namespace)


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_certificate_renewals_reported(mdb: MongoDB, namespace: str):
    """The operator reports when each category will next renew, so users can see the schedule.

    The server entry names the member certificate, the client entry names one of the two client
    certificates, and the reported server renewal is cert-manager's own status for that Certificate.
    """
    renewals = wait_for_certificate_renewals(mdb)
    member_name, clusterfile_name, agent_name = leaf_secret_names(MDB_RESOURCE)

    server = renewals.get("server")
    assert server is not None, "status.certificateRenewals.server must be reported"
    assert server["name"] == member_name
    assert server.get("renewalTime"), "the server renewal must carry the next renewal time"
    assert server.get("notAfter"), "the server renewal must carry the certificate expiry"

    member_status = read_certificate_status(namespace, member_name)
    assert member_status.get("renewalTime"), "cert-manager must have scheduled the member renewal"
    assert _as_time(server["renewalTime"]) == _as_time(member_status["renewalTime"])

    client = renewals.get("client")
    assert client is not None, "status.certificateRenewals.client must be reported"
    assert client["name"] in {clusterfile_name, agent_name}
    assert client.get("renewalTime"), "the client renewal must carry the next renewal time"
    assert client.get("notAfter"), "the client renewal must carry the certificate expiry"

    # The renewal columns are wide-only (priority 1), so they do not clutter the default output.
    for name, json_path in (
        ("ServerCertRenewal", ".status.certificateRenewals.server.renewalTime"),
        ("ClientCertRenewal", ".status.certificateRenewals.client.renewalTime"),
    ):
        column = _printer_column(name)
        assert column.priority == 1, f"{name} must be a priority 1 printer column"
        assert column.json_path == json_path


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_automatic_renewal(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    """A short-lived certificate is renewed by cert-manager's timer, then rolled without new pods.

    Changing the lifetime only affects the next issuance, so the test first forces one issuance to
    put a short-lived certificate in place. From that point on it only observes: cert-manager
    reissues on its own schedule and the operator rolls the members onto the new files.
    """
    mdb.load()
    member_name, _, _ = leaf_secret_names(MDB_RESOURCE)
    member_pem = f"{member_name}-pem"

    ac_before = mdb.get_automation_config_tester().automation_config["version"]
    revision_before, member_crt_before = member_cert_issuance(MDB_RESOURCE, namespace)
    latest_before, _ = pem_secret_hashes(namespace, member_pem)
    renewals_before = wait_for_certificate_renewals(mdb)
    pod_uids_before = _pod_uids(mdb)
    assert pod_uids_before, "the replica set pods must exist before the renewal"

    mdb["spec"]["security"]["managedCertificate"]["server"]["duration"] = SHORT_DURATION
    mdb["spec"]["security"]["managedCertificate"]["server"]["renewBefore"] = SHORT_RENEW_BEFORE
    mdb.update()

    # Wait for the operator to copy the new lifetime onto the Certificate, then force one issuance
    # so the already-issued long-lived certificate is replaced by a short-lived one.
    wait_for_certificate_spec_fields(namespace, member_name, ["duration", "renewBefore"])
    trigger_certificate_renewal(namespace, member_name)

    revision_first, latest_first, previous_first = wait_for_certificate_issuance_after(
        namespace, member_name, member_pem, revision_before, latest_before, timeout=300
    )
    assert revision_first > revision_before
    assert latest_first != latest_before
    assert previous_first == latest_before, "the -pem secret must keep the certificate it just replaced"
    assert latest_before in read_secret(namespace, member_pem), "the old certificate must still be on disk"
    _, member_crt_first = member_cert_issuance(MDB_RESOURCE, namespace)
    assert member_crt_first != member_crt_before, "the member must serve a newly issued certificate"

    wait_for_automation_config_version_above(mdb, ac_before, timeout=600)
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_automation_config_to_use_leaf_secrets(mdb, namespace)

    # The shortened lifetime must have moved the next renewal close to now; otherwise the wait
    # below would time out instead of proving an automatic renewal.
    initial_renewal = _as_time(renewals_before["server"]["renewalTime"])
    run_periodically(
        lambda: _server_renewal_time(mdb) < initial_renewal,
        timeout=600,
        sleep_time=5,
        msg="the shortened lifetime to move the next server renewal forward",
    )

    # From here on the test does nothing to the Certificate: the next issuance is cert-manager's own
    # automatic renewal, not another manual trigger.
    ac_before_auto = mdb.get_automation_config_tester().automation_config["version"]
    renewal_before_auto = _server_renewal_time(mdb)
    revision_before_auto, latest_before_auto = wait_for_certificate_issuance_settled(namespace, member_name, member_pem)

    revision_auto, latest_auto, previous_auto = wait_for_certificate_issuance_after(
        namespace, member_name, member_pem, revision_before_auto, latest_before_auto, timeout=600
    )
    assert revision_auto > revision_before_auto
    assert latest_auto != latest_before_auto
    assert previous_auto == latest_before_auto, "the automatic renewal must keep the certificate it replaced"
    assert latest_before_auto in read_secret(namespace, member_pem)
    _, member_crt_auto = member_cert_issuance(MDB_RESOURCE, namespace)
    assert member_crt_auto != member_crt_first, "the automatic renewal must replace the certificate's content"

    wait_for_automation_config_version_above(mdb, ac_before_auto, timeout=600)
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_automation_config_to_use_leaf_secrets(mdb, namespace)
    run_periodically(
        lambda: _server_renewal_time(mdb) > renewal_before_auto,
        timeout=600,
        sleep_time=5,
        msg="the next server renewal to move forward after the automatic renewal",
    )

    # The agent drives the roll: the pods keep their identity while their processes pick up the new
    # certificate files. A pod recreation would show up as a changed UID and restart the deployment.
    assert _pod_uids(mdb) == pod_uids_before, "a certificate renewal must not recreate the pods"
    _assert_reachable(mdb, app_user, namespace)


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_manual_renewal(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    """A manual renewal reissues the client certificate and the operator rolls it the same way.

    The clusterfile certificate is client-signed and does not auto-renew during this module, so the
    test can force it in isolation and check that the new hash reaches the automation config.
    """
    _, clusterfile_name, _ = leaf_secret_names(MDB_RESOURCE)
    clusterfile_pem = f"{clusterfile_name}-pem"

    ac_before = mdb.get_automation_config_tester().automation_config["version"]
    revision_before = read_certificate_status(namespace, clusterfile_name).get("revision", 0)
    latest_before, _ = pem_secret_hashes(namespace, clusterfile_pem)

    trigger_certificate_renewal(namespace, clusterfile_name)

    revision_after, latest_after, previous_after = wait_for_certificate_issuance_after(
        namespace, clusterfile_name, clusterfile_pem, revision_before, latest_before, timeout=600
    )
    assert revision_after > revision_before
    assert latest_after != latest_before
    assert previous_after == latest_before
    assert latest_before in read_secret(namespace, clusterfile_pem), "the old certificate must still be on disk"

    wait_for_automation_config_version_above(mdb, ac_before, timeout=600)
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_automation_config_to_use_leaf_secrets(mdb, namespace)
    _assert_reachable(mdb, app_user, namespace)


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_restore_default_durations(mdb: MongoDB, namespace: str):
    """Puts the default lifetime back before the failure scenario, so no background renewal races it.

    Removing the short lifetime only applies to the next issuance: the current one-hour certificate would
    still renew on its own during the failure scenario. One issuance is forced so a default-lifetime
    certificate is in place, whose next renewal is far away.
    """
    member_name, _, _ = leaf_secret_names(MDB_RESOURCE)
    member_pem = f"{member_name}-pem"

    mdb.load()
    server = mdb["spec"]["security"]["managedCertificate"]["server"]
    # The resource is updated with a merge patch, where null removes the field.
    server["duration"] = None
    server["renewBefore"] = None
    mdb.update()
    wait_for_certificate_spec_fields(namespace, member_name, ["duration", "renewBefore"], present=False, timeout=600)

    revision_before, latest_before = wait_for_certificate_issuance_settled(namespace, member_name, member_pem)
    trigger_certificate_renewal(namespace, member_name)
    wait_for_certificate_issuance_after(namespace, member_name, member_pem, revision_before, latest_before, timeout=600)
    wait_for_certificate_issuance_settled(namespace, member_name, member_pem)
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_certificates_ready_condition(mdb, "True", "AllCertificatesReady", timeout=600)


@pytest.mark.e2e_replica_set_tls_managedcert_renewal
def test_renewal_failure_is_reported_without_failing_the_resource(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    """A broken issuer is a soft failure: the current certificate keeps the deployment running.

    Replacing the issuer's CA private key makes every signature fail while the already-issued
    certificate stays valid. The operator must report CertificatesReady=False/RenewalFailed and keep the phase
    Running; once the CA is back, the retry must recover and report all certificates ready again.
    """
    member_name, _, _ = leaf_secret_names(MDB_RESOURCE)
    member_pem = f"{member_name}-pem"

    assert not read_certificate_status(namespace, member_name).get(
        "lastFailureTime"
    ), "no failed renewal expected before breaking the issuer"

    ca_secret_name, ca_secret_data = break_issuer_ca(namespace)
    try:
        # Capture the state after the issuer is unable to sign, so the recovery below is not
        # satisfied by an issuance that was already in flight before the CA was broken.
        revision_before, latest_before = wait_for_certificate_issuance_settled(namespace, member_name, member_pem)

        trigger_certificate_renewal(namespace, member_name)

        wait_for_certificate_failure(namespace, member_name, timeout=600)
        status_failing = read_certificate_status(namespace, member_name)
        assert status_failing.get("lastFailureTime")
        # cert-manager keeps Ready=True because the secret still holds a usable certificate; only the
        # renewal attempt failed.
        assert _certificate_condition(status_failing, "Ready")["status"] == "True"

        wait_for_certificates_ready_condition(mdb, "False", "RenewalFailed", timeout=300)
        mdb.assert_reaches_phase(Phase.Running, timeout=300)
        _assert_reachable(mdb, app_user, namespace)
    finally:
        restore_issuer_ca(namespace, ca_secret_name, ca_secret_data)

    # cert-manager backs off for up to an hour after a failed issuance, so the failure is cleared and the
    # issuance retried until it succeeds with the CA available again.
    retry_issuance_until_success(namespace, member_name)

    revision_after, latest_after, _ = wait_for_certificate_issuance_after(
        namespace, member_name, member_pem, revision_before, latest_before, timeout=600
    )
    assert revision_after > revision_before
    assert latest_after != latest_before

    # A successful issuance clears lastFailureTime, so AllCertificatesReady is the proof that the
    # retry really succeeded rather than an earlier in-flight issuance hiding the failure.
    wait_for_certificates_ready_condition(mdb, "True", "AllCertificatesReady", timeout=600)
    assert not read_certificate_status(namespace, member_name).get("lastFailureTime")
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
    _assert_reachable(mdb, app_user, namespace)
