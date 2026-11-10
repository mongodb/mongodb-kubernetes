"""Shared helpers for the operator-managed certificate (security.managedCertificate) replica set tests.

Each test file deploys the same replica set with a different managedCertificate block (which
category, if any, uses a user configured issuer), so the resource setup and the common checks
live here and the test files only keep what is specific to their scenario.
"""

import hashlib
import re
import ssl
import tempfile
from datetime import datetime, timezone
from typing import Any, List, Optional

from cryptography import x509
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec, rsa
from cryptography.x509.oid import NameOID
from kubernetes import client
from kubetester import create_or_update_secret, read_configmap, read_secret, try_load
from kubetester.certs import Certificate, Issuer
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import with_x509
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import get_default_operator, get_operator_installation_config

# The user configured issuer the `issuer` fixture creates. It is a CA-type Issuer, so it writes its
# CA into each issued secret's ca.crt.
CA_ISSUER_REF = {"issuerRef": {"name": "ca-issuer", "kind": "Issuer"}}


# APP_USER is the MongoDBUser an application connects as, with an operator-issued client certificate.
APP_USER = "app-user"


def client_certificate_user(mdb: MongoDB, namespace: str) -> MongoDBUser:
    """Creates a MongoDBUser with spec.clientCertificate and waits until it is registered.

    The operator issues its x509 client certificate and registers it under the certificate subject,
    so no username or password is given.
    """
    user = MongoDBUser(name=APP_USER, namespace=namespace)
    user["spec"] = {
        "db": "$external",
        "mongodbResourceRef": {"name": mdb.name},
        # assert_connectivity writes to the admin database, which readWriteAnyDatabase does not cover.
        "roles": [{"db": "admin", "name": "readWrite"}],
        "clientCertificate": {},
    }
    try_load(user)
    user.update()
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    return user


def assert_reachable_with_client_certificate(
    mdb: MongoDB,
    user: MongoDBUser,
    namespace: str,
    service_names: Optional[List[str]] = None,
    attempts: int = 50,
):
    """Connects as the user with its operator-issued certificate, the way an application would.

    The user's status names the two things an application needs: the Secret with its certificate and
    key, and the ConfigMap with the CA that verifies the server. mongod requires a client certificate
    because x509 is the only authentication mode, and checks it against the client CA.

    service_names is only needed for a multi-cluster sharded deployment, which has no single mongos
    service: the caller passes the service of each mongos pod. Each failed attempt waits for the
    driver's server selection timeout, so lower attempts to fail fast where the deployment is
    expected to be reachable already.
    """
    user.load()
    cert_status = user["status"]["clientCertificate"]
    cert = read_secret(namespace, cert_status["secretName"])
    # Applications read the fixed ca-pem key the user status documents, not the hash-named copy mongod uses.
    server_ca = read_configmap(namespace, cert_status["serverCAConfigMapName"])["ca-pem"]

    with (
        tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as cert_file,
        tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as ca_file,
    ):
        # Drivers take the certificate and key as one PEM file.
        cert_file.write(cert["tls.crt"] + cert["tls.key"])
        ca_file.write(server_ca)

    mdb.tester(use_ssl=True, ca_path=ca_file.name, service_names=service_names).assert_connectivity(
        attempts=attempts, opts=[with_x509(cert_file.name, ca_file.name)]
    )


def managed_certificate_operator(namespace: str) -> Operator:
    """Installs the operator with operator-managed certificates enabled.

    The chart grants the operator the cert-manager RBAC from this same value, and the operator reads
    it to know whether it may use cert-manager at all. The operator works that out once, at startup,
    so the value has to be set when it is installed, and cert-manager has to be installed before it:
    the test modules that use this install the operator from a fixture that depends on cert_manager.
    """
    operator_installation_config = get_operator_installation_config(namespace)
    operator_installation_config["security.managedCertificate.enabled"] = "true"
    return get_default_operator(namespace, operator_installation_config)


def managed_cert_replica_set(name: str, namespace: str, mdb_version: str, managed_certificate: dict) -> MongoDB:
    """A TLS replica set with X509 member and internal auth, using operator-managed certificates."""
    resource = MongoDB.from_yaml(load_fixture("test-x509-all-options-rs.yaml"), name=name, namespace=namespace)
    resource.set_version(mdb_version)
    resource["spec"]["security"]["managedCertificate"] = managed_certificate
    try_load(resource)
    return resource


def leaf_secret_names(name: str) -> tuple[str, str, str]:
    """The member (server), clusterfile and agent certificate secrets the operator creates."""
    return f"{name}-managed-cert", f"{name}-managed-clusterfile", f"{name}-managed-agent-certs"


def certificate_extended_key_usages(cert_pem: str) -> set:
    """The extended key usages of a PEM certificate."""
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    extension = certificate.extensions.get_extension_for_class(x509.ExtendedKeyUsage)
    return set(extension.value)


def assert_member_certificate_client_usage(name: str, namespace: str, *, client_auth: bool):
    """Checks the client usage on the member certificate.

    The members present the member certificate when they connect to each other only when there is no
    clusterfile and the mongod configuration does not withhold it, so only then does the certificate
    have to carry the client usage.
    """
    usages = certificate_extended_key_usages(read_secret(namespace, f"{name}-managed-cert")["tls.crt"])
    assert (
        x509.oid.ExtendedKeyUsageOID.SERVER_AUTH in usages
    ), "the member certificate has to be usable as a server certificate"
    if client_auth:
        assert (
            x509.oid.ExtendedKeyUsageOID.CLIENT_AUTH in usages
        ), "the members present the member certificate to each other, so it has to carry the client usage"
    else:
        assert (
            x509.oid.ExtendedKeyUsageOID.CLIENT_AUTH not in usages
        ), "the members withhold the member certificate, so the operator must not request the client usage"


def assert_leaf_secrets_created(name: str, namespace: str):
    """Every leaf certificate is issued into a kubernetes.io/tls secret, with no user created secret."""
    for secret_name in leaf_secret_names(name):
        secret = read_secret(namespace, secret_name)
        assert "tls.crt" in secret, f"{secret_name} missing tls.crt"
        assert "tls.key" in secret, f"{secret_name} missing tls.key"


def latest_ca_bundle(bundle_configmap: dict) -> str:
    """Returns the current CA bundle from a managed CA bundle ConfigMap's data.

    The bundle is stored under its content hash (the file name in the pod, and what the automation
    config points at), and the `latestHash` key names the current one.
    """
    latest_hash = bundle_configmap.get("latestHash", "")
    assert latest_hash != "", "latestHash missing/empty"
    return bundle_configmap.get(latest_hash, "").strip()


def read_ca_bundles(name: str, namespace: str) -> tuple[str, str]:
    """Returns the server and client CA bundles the operator writes for the pods to trust.

    <name>-managed-server-ca-bundle is configured as net.tls.CAFile, and
    <name>-managed-client-ca-bundle as net.tls.clusterCAFile. In both the bundle is stored under
    its content hash, and the `latestHash` key says which one is current. Both must exist and be
    non-empty.
    """
    server_ca = latest_ca_bundle(read_configmap(namespace, f"{name}-managed-server-ca-bundle"))
    client_ca = latest_ca_bundle(read_configmap(namespace, f"{name}-managed-client-ca-bundle"))
    assert server_ca != "", "latest server CA bundle missing/empty"
    assert client_ca != "", "latest client CA bundle missing/empty"
    return server_ca, client_ca


# The annotation that asks the operator to drop the previous CA it retains in the CA bundles after a CA
# rotation. "true" prunes both categories, "server" or "client" only that one. The operator does not
# remove the annotation from the resource.
PRUNE_PREVIOUS_CA_ANNOTATION = "mongodb.com/prune-previous-ca"


def bundle_fingerprints(bundle: str) -> List[str]:
    """The SHA-256 fingerprints (hex of the DER) of the certificates currently in a PEM bundle."""
    pems = re.findall(r"-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----", bundle, re.DOTALL)
    return [hashlib.sha256(ssl.PEM_cert_to_DER_cert(pem)).hexdigest() for pem in pems]


def _bundle_contains(bundle: str, ca_pem: str) -> bool:
    expected = set(bundle_fingerprints(ca_pem))
    assert expected, "the CA to look for is not a PEM certificate"
    return expected.issubset(set(bundle_fingerprints(bundle)))


def assert_previous_ca_retained(resource: MongoDB, category: str, previous_ca: str):
    """The category's published CA bundle still trusts previous_ca (PEM) after a rotation."""
    bundle = latest_ca_bundle(read_configmap(resource.namespace, f"{resource.name}-managed-{category}-ca-bundle"))
    assert _bundle_contains(bundle, previous_ca), f"the {category} CA bundle must still contain the previous CA"


def assert_previous_ca_pruned(resource: MongoDB, category: str, previous_ca: str):
    """The category's published CA bundle no longer trusts previous_ca (PEM)."""
    bundle = latest_ca_bundle(read_configmap(resource.namespace, f"{resource.name}-managed-{category}-ca-bundle"))
    assert not _bundle_contains(
        bundle, previous_ca
    ), f"the {category} CA bundle must not contain the previous CA anymore"


def request_prune_previous_ca(resource: MongoDB, value: str = "true"):
    """Annotates the resource so that the operator prunes the retained previous CA(s)."""
    resource.load()
    resource["metadata"].setdefault("annotations", {})[PRUNE_PREVIOUS_CA_ANNOTATION] = value
    resource.update()


def assert_self_signed_ca_created(name: str, namespace: str, category: str):
    """The operator provisioned its own root CA for a category ("server" or "client") with no issuer."""
    secret_name = f"{name}-{category}-ca"
    assert "tls.crt" in read_secret(namespace, secret_name), f"{secret_name} missing tls.crt"


def assert_leaf_certs_signed_by_its_category_ca(name: str, namespace: str, server_ca: str, client_ca: str):
    """Each leaf cert is signed by its own category's CA and not by the other one.

    The member cert is a server cert, so it is signed by the server CA. The clusterfile and agent
    certs are client certs, so they are signed by the client CA. This checks the signatures, not
    just that the secrets exist.
    """
    member_name, clusterfile_name, agent_name = leaf_secret_names(name)
    member_crt = read_secret(namespace, member_name)["tls.crt"]
    clusterfile_crt = read_secret(namespace, clusterfile_name)["tls.crt"]
    agent_crt = read_secret(namespace, agent_name)["tls.crt"]

    assert cert_signed_by_bundle(member_crt, server_ca), "member (server) cert must be signed by the server CA"
    assert not cert_signed_by_bundle(member_crt, client_ca), "member cert must not be signed by the client CA"
    assert cert_signed_by_bundle(clusterfile_crt, client_ca), "clusterfile cert must be signed by the client CA"
    assert cert_signed_by_bundle(agent_crt, client_ca), "agent cert must be signed by the client CA"
    assert not cert_signed_by_bundle(clusterfile_crt, server_ca), "clusterfile cert must not be signed by the server CA"


def read_certificate_renewals(resource: MongoDB) -> Optional[dict]:
    """Returns status.certificateRenewals, or None when the operator has not set it.

    The operator reports, per category, the leaf certificate that cert-manager renews soonest and
    when: renewalTime is when the members roll onto the new certificate.
    """
    resource.load()
    if "status" not in resource:
        return None
    return resource["status"].get("certificateRenewals")


def member_cert_issuance(name: str, namespace: str) -> tuple[int, str]:
    """Returns how many times the member cert has been issued, and its current content.

    cert-manager adds one to the Certificate's status.revision every time it issues the cert, so the
    revision counts issuances exactly. The content is returned too, as a second check that the cert
    did not change.
    """
    member_name = leaf_secret_names(name)[0]
    certificate = Certificate(name=member_name, namespace=namespace).load()
    revision = certificate["status"].get("revision", 0) if "status" in certificate else 0
    return revision, read_secret(namespace, member_name)["tls.crt"]


def scale_and_wait_for_running(mdb: MongoDB, members: int, timeout: int = 900):
    mdb["spec"]["members"] = members
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=timeout)


def scale_and_assert_member_cert_issuance(mdb: MongoDB, name: str, namespace: str, members: int, *, reissued: bool):
    """Scales the replica set, then checks how many times the member cert was issued.

    reissued=True is for a scale up past the largest size reached so far: the operator adds members one
    at a time, but the cert covers the target from the first step, so it is issued exactly once for the
    whole scale up, not once per member.

    reissued=False is for a scale down, or a scale up that stays within the largest size reached so far:
    the operator never shrinks the member cert SANs, so the cert already covers every member and must not
    be issued again. cert-manager's revision is unchanged and the content is byte-for-byte the same.
    """
    revision_before, cert_before = member_cert_issuance(name, namespace)

    scale_and_wait_for_running(mdb, members)

    revision_after, cert_after = member_cert_issuance(name, namespace)
    if reissued:
        assert revision_after == revision_before + 1, "member cert must be issued once for the whole scale up"
        assert cert_after != cert_before, "member cert must be reissued to cover the new members"
    else:
        assert revision_after == revision_before, "member cert must not be reissued"
        assert cert_after == cert_before, "member cert must not change"


# Turns off the operator's automatic recovery. Tests of the legacy to managedCertificate migration
# need it off: recovery writes every automation config change at once after 20 minutes, which would
# hide a migration that deadlocks. The helm command runs in a shell, so the & that separates the
# variables in customEnvVars is escaped.
RECOVERY_OFF_ENV_VAR = "\\&MDB_AUTOMATIC_RECOVERY_ENABLE=false"

# The subject attributes a certificate can carry, mapped from the field name the API uses for it to
# the X.509 OID that carries it.
_SUBJECT_ATTRIBUTES = {
    "commonName": NameOID.COMMON_NAME,
    "organizations": NameOID.ORGANIZATION_NAME,
    "organizationalUnits": NameOID.ORGANIZATIONAL_UNIT_NAME,
    "countries": NameOID.COUNTRY_NAME,
    "provinces": NameOID.STATE_OR_PROVINCE_NAME,
    "localities": NameOID.LOCALITY_NAME,
}


def subject_from_certificate(namespace: str, secret_name: str, api_client=None) -> dict:
    """The subject of the certificate a secret holds, in the shape managedCertificate.subjects expects.

    It reads the certificate the legacy deployment actually uses, so the migration keeps the subject
    that is in use, instead of repeating what the test used to create it.
    """
    certificate = read_secret(namespace, secret_name, api_client=api_client)["tls.crt"]
    subject = x509.load_pem_x509_certificate(certificate.encode()).subject

    # Only the attributes the certificate carries, so the operator keeps its defaults for the rest.
    # commonName is a single value in the API, the other attributes are lists.
    attributes: dict[str, Any] = {}
    for api_field, oid in _SUBJECT_ATTRIBUTES.items():
        values = [attribute.value for attribute in subject.get_attributes_for_oid(oid)]
        if values:
            attributes[api_field] = values[0] if api_field == "commonName" else values
    return attributes


def migration_managed_certificate(membership: dict, agent: dict) -> dict:
    """The managedCertificate block a legacy deployment migrates to, without a CA change.

    Both categories are signed by the issuer that signed the legacy certificates (ca-issuer), and the
    subjects are the ones the legacy certificates carry, so the members keep trusting each other and
    recognising each other as peers while the pods restart one at a time.
    """
    return {
        "server": CA_ISSUER_REF,
        "client": CA_ISSUER_REF,
        "subjects": {"membership": membership, "agent": agent},
    }


def assert_automation_config_names_files(agent_hash: str, member_hashes: dict, clusterfile_hashes: dict):
    """The automation config has to name exactly the certificate files the pods have.

    The file names are the hashes in each -pem secret's latestHash key. member_hashes and
    clusterfile_hashes map a process name prefix (the StatefulSet the process runs in) to the hash of
    that StatefulSet's -pem secret, because a sharded cluster has one secret per component.
    """
    ac = KubernetesTester.get_automation_config()
    assert ac["tls"]["autoPEMKeyFilePath"].endswith(agent_hash), ac["tls"]["autoPEMKeyFilePath"]
    for process in ac["processes"]:
        name = process["name"]
        tls = process["args2_6"]["net"]["tls"]
        prefix = _longest_matching_prefix(name, member_hashes)
        assert tls["certificateKeyFile"].endswith(member_hashes[prefix]), f"{name}: {tls['certificateKeyFile']}"
        if "clusterFile" in tls:
            assert tls["clusterFile"].endswith(clusterfile_hashes[prefix]), f"{name}: {tls['clusterFile']}"


def _longest_matching_prefix(process_name: str, by_prefix: dict) -> str:
    # Process names look like <statefulset>-<index>, sometimes after a k8s/<namespace>/ prefix, and
    # one StatefulSet name can be a prefix of another, so the longest match is the right one.
    pod_name = process_name.split("/")[-1]
    matches = [prefix for prefix in by_prefix if pod_name.startswith(prefix + "-")]
    assert matches, f"no certificate secret matches process {process_name}: {sorted(by_prefix)}"
    return max(matches, key=len)


# cert-manager Certificate resources, as the status subresource patch needs to name them.
_CERTIFICATE_GROUP = "cert-manager.io"
_CERTIFICATE_VERSION = "v1"
_CERTIFICATE_PLURAL = "certificates"


def read_certificate_status(namespace: str, certificate_name: str) -> dict:
    """The cert-manager Certificate's status, or an empty dict before its first reconciliation."""
    certificate = Certificate(name=certificate_name, namespace=namespace).load()
    return certificate["status"] if "status" in certificate else {}


def read_certificate_spec(namespace: str, certificate_name: str) -> dict:
    """The cert-manager Certificate's spec as the operator has applied it."""
    certificate = Certificate(name=certificate_name, namespace=namespace).load()
    return certificate["spec"] if "spec" in certificate else {}


def pem_secret_hashes(namespace: str, secret_name: str) -> tuple[str, str]:
    """The current and previous certificate hashes in an operator-managed -pem secret.

    The hash names the file the pods mount and the automation config points at, so a change means
    the operator has started using a new certificate. previousHash is empty until the first rotation.
    """
    secret = read_secret(namespace, secret_name)
    return secret.get("latestHash", ""), secret.get("previousHash", "")


def wait_for_certificate_spec_fields(
    namespace: str, certificate_name: str, fields: List[str], *, present: bool = True, timeout: int = 300
):
    """Waits until the operator applies or removes fields on the cert-manager Certificate.

    The operator copies duration/renewBefore from the resource onto the Certificate, so the
    Certificate spec is the proof that a resource change has reached cert-manager.
    """

    def matches() -> bool:
        spec = read_certificate_spec(namespace, certificate_name)
        if present:
            return all(bool(spec.get(field)) for field in fields)
        return all(not spec.get(field) for field in fields)

    run_periodically(matches, timeout=timeout, sleep_time=3, msg=f"Certificate/{certificate_name} spec {fields}")


def wait_for_certificate_renewals(resource: MongoDB, timeout: int = 300) -> dict:
    """Returns status.certificateRenewals once the operator has reported it."""
    run_periodically(
        lambda: read_certificate_renewals(resource) is not None,
        timeout=timeout,
        sleep_time=3,
        msg="status.certificateRenewals",
    )
    renewals = read_certificate_renewals(resource)
    assert renewals is not None
    return renewals


def trigger_certificate_renewal(namespace: str, certificate_name: str, api_client=None):
    """Asks cert-manager to reissue now, the way `cmctl renew` does.

    Setting the Issuing condition on the status is the documented manual renewal trigger. The other
    conditions are kept, so the Certificate keeps reporting its current health while it reissues.
    """
    certificate = Certificate(name=certificate_name, namespace=namespace).load()
    status = certificate["status"] if "status" in certificate else {}
    conditions = [condition for condition in status.get("conditions", []) if condition.get("type") != "Issuing"]
    issuing = {
        "type": "Issuing",
        "status": "True",
        "reason": "ManuallyTriggered",
        "message": "Certificate re-issuance manually triggered by the e2e test",
        "lastTransitionTime": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    generation = certificate["metadata"].get("generation")
    if generation is not None:
        issuing["observedGeneration"] = generation
    conditions.append(issuing)

    client.CustomObjectsApi(api_client=api_client).patch_namespaced_custom_object_status(
        _CERTIFICATE_GROUP,
        _CERTIFICATE_VERSION,
        namespace,
        _CERTIFICATE_PLURAL,
        certificate_name,
        body={"status": {"conditions": conditions}},
    )


def clear_certificate_failure(namespace: str, certificate_name: str, api_client=None):
    """Clears the failure state so cert-manager does not back off for an hour after a broken issuer.

    cert-manager records lastFailureTime on a failed issuance and delays the next attempt with an
    exponential backoff. A test that restores the issuer cannot wait that long, so it clears the
    failure before triggering the retry.
    """
    client.CustomObjectsApi(api_client=api_client).patch_namespaced_custom_object_status(
        _CERTIFICATE_GROUP,
        _CERTIFICATE_VERSION,
        namespace,
        _CERTIFICATE_PLURAL,
        certificate_name,
        body={"status": {"lastFailureTime": None, "failedIssuanceAttempts": None}},
    )


def wait_for_certificate_failure(namespace: str, certificate_name: str, timeout: int = 600):
    """Waits until cert-manager records a failed issuance on the Certificate.

    cert-manager reads the issuer's CA through its informer cache, so an issuance triggered right after
    the CA was broken can still be signed with the old key. When an attempt finishes without a failure,
    it is triggered again until one fails.
    """

    def failed() -> bool:
        status = read_certificate_status(namespace, certificate_name)
        if status.get("lastFailureTime"):
            return True
        issuing = any(c.get("type") == "Issuing" and c.get("status") == "True" for c in status.get("conditions", []))
        if not issuing:
            trigger_certificate_renewal(namespace, certificate_name)
        return False

    run_periodically(
        failed, timeout=timeout, sleep_time=5, msg=f"Certificate/{certificate_name} to record a failed issuance"
    )


def delete_failed_certificate_requests(namespace: str, certificate_name: str, api_client=None) -> int:
    """Deletes the Certificate's failed CertificateRequests, so the next issuance creates a new one.

    cert-manager only replaces a failed request of the current revision when the Issuing condition is
    newer than the request's failure, which an external trigger cannot rely on. Without a request to
    reuse, cert-manager has to create a new one, signed by the issuer as it is now.
    """
    api = client.CustomObjectsApi(api_client=api_client)
    requests = api.list_namespaced_custom_object(
        _CERTIFICATE_GROUP, _CERTIFICATE_VERSION, namespace, "certificaterequests"
    )["items"]
    deleted = 0
    for request in requests:
        annotations = request["metadata"].get("annotations") or {}
        if annotations.get("cert-manager.io/certificate-name") != certificate_name:
            continue
        conditions = (request.get("status") or {}).get("conditions", [])
        if any(c.get("type") == "Ready" and c.get("reason") in ("Failed", "Denied") for c in conditions):
            api.delete_namespaced_custom_object(
                _CERTIFICATE_GROUP, _CERTIFICATE_VERSION, namespace, "certificaterequests", request["metadata"]["name"]
            )
            deleted += 1
    return deleted


def retry_issuance_until_success(namespace: str, certificate_name: str, timeout: int = 600):
    """Retries the issuance after the issuer was repaired, until an attempt no longer fails.

    Each failed attempt makes cert-manager back off, for up to an hour, and its informer cache can still
    hold the broken CA for the first retry. So every failed request is deleted, the recorded failure is
    cleared and the issuance is triggered again, until an attempt succeeds.
    """

    def retry():
        delete_failed_certificate_requests(namespace, certificate_name)
        clear_certificate_failure(namespace, certificate_name)
        trigger_certificate_renewal(namespace, certificate_name)

    def retried() -> bool:
        status = read_certificate_status(namespace, certificate_name)
        issuing = any(c.get("type") == "Issuing" and c.get("status") == "True" for c in status.get("conditions", []))
        if issuing:
            return False
        if status.get("lastFailureTime"):
            retry()
            return False
        return True

    retry()
    run_periodically(
        retried, timeout=timeout, sleep_time=5, msg=f"Certificate/{certificate_name} to issue again without a failure"
    )


def wait_for_certificate_issuance_settled(
    namespace: str, certificate_name: str, pem_secret_name: str, timeout: int = 300
) -> tuple[int, str]:
    """Returns the Certificate revision and -pem hash once both stopped changing.

    cert-manager bumps the revision before the operator has written the new hash into the -pem
    secret. A test that wants to observe the next issuance needs a baseline where the two agree,
    otherwise it can mistake the operator's lag for the next issuance.
    """
    state: dict[str, Optional[tuple[int, str]]] = {"last": None}

    def settled() -> bool:
        revision = read_certificate_status(namespace, certificate_name).get("revision", 0)
        latest, _ = pem_secret_hashes(namespace, pem_secret_name)
        if not latest:
            return False
        current = (revision, latest)
        if state["last"] == current:
            return True
        state["last"] = current
        return False

    run_periodically(
        settled,
        timeout=timeout,
        sleep_time=2,
        msg=f"Certificate/{certificate_name} and its -pem secret to stop changing",
    )
    last = state["last"]
    assert last is not None, "run_periodically only returns once a settled state was recorded"
    return last


def wait_for_certificate_issuance_after(
    namespace: str,
    certificate_name: str,
    pem_secret_name: str,
    after_revision: int,
    previous_hash: str,
    timeout: int = 600,
) -> tuple[int, str, str]:
    """Waits for a new issuance and for the operator to put its files in the -pem secret.

    cert-manager increments status.revision per successful issuance, and then the operator writes the
    new certificate under the hash in the -pem secret. Both have to change: waiting for the revision
    alone can observe the moment before the operator has updated the files the pods use.
    """
    result = {}

    def issued() -> bool:
        revision = read_certificate_status(namespace, certificate_name).get("revision", 0)
        if revision <= after_revision:
            return False
        latest, previous = pem_secret_hashes(namespace, pem_secret_name)
        if not latest or latest == previous_hash:
            return False
        result["revision"] = revision
        result["latest"] = latest
        result["previous"] = previous
        return True

    run_periodically(
        issued,
        timeout=timeout,
        sleep_time=5,
        msg=f"Certificate/{certificate_name} revision past {after_revision} and new -pem hash",
    )
    return result["revision"], result["latest"], result["previous"]


def certificates_ready_condition(resource: MongoDB) -> Optional[dict]:
    """Returns the resource's CertificatesReady status condition, or None when it is not set yet."""
    resource.load()
    if "status" not in resource:
        return None
    for condition in resource["status"].get("conditions", []):
        if condition.get("type") == "CertificatesReady":
            return condition
    return None


def wait_for_certificates_ready_condition(
    resource: MongoDB, status: str, reason: Optional[str] = None, timeout: int = 300
) -> dict:
    """Waits until CertificatesReady reports status (and reason when given) and returns the condition."""

    def matches() -> bool:
        condition = certificates_ready_condition(resource)
        if condition is None or condition.get("status") != status:
            return False
        return reason is None or condition.get("reason") == reason

    run_periodically(
        matches, timeout=timeout, sleep_time=5, msg=f"{resource.name} CertificatesReady={status} {reason or ''}"
    )
    condition = certificates_ready_condition(resource)
    assert condition is not None
    return condition


def wait_for_automation_config_version_above(resource: MongoDB, version: int, timeout: int = 600) -> int:
    """Waits until the automation config version advances, which is how the operator applies new certs."""
    run_periodically(
        lambda: resource.get_automation_config_tester().automation_config["version"] > version,
        timeout=timeout,
        sleep_time=5,
        msg=f"{resource.name} automation config version above {version}",
    )
    return resource.get_automation_config_tester().automation_config["version"]


def assert_automation_config_uses_leaf_secrets(resource: MongoDB, namespace: str):
    """The automation config must name the current member, clusterfile and agent certificate files."""
    member_name, clusterfile_name, agent_name = leaf_secret_names(resource.name)
    agent_hash, _ = pem_secret_hashes(namespace, f"{agent_name}-pem")
    member_hash, _ = pem_secret_hashes(namespace, f"{member_name}-pem")
    clusterfile_hash, _ = pem_secret_hashes(namespace, f"{clusterfile_name}-pem")
    assert_automation_config_names_files(agent_hash, {resource.name: member_hash}, {resource.name: clusterfile_hash})


def wait_for_automation_config_to_use_leaf_secrets(resource: MongoDB, namespace: str, timeout: int = 600):
    """Waits until the automation config points at the certificates currently in the -pem secrets."""

    def names_current_files() -> bool:
        try:
            assert_automation_config_uses_leaf_secrets(resource, namespace)
            return True
        except (AssertionError, KeyError):
            return False

    run_periodically(
        names_current_files,
        timeout=timeout,
        sleep_time=5,
        msg=f"{resource.name} automation config to point at the current certificates",
    )


def break_issuer_ca(namespace: str, issuer_name: str = "ca-issuer") -> tuple[str, dict]:
    """Makes the user issuer unable to sign, returning what is needed to restore it.

    The CA certificate is kept but its private key is replaced by an unrelated one. cert-manager only
    checks that the CA Secret has a CA certificate and a key, so the Issuer stays Ready, but every
    signature then fails: the request ends Failed (SigningError) and the Certificate records a failed
    issuance. Deleting the Secret instead would only leave the request Pending, with no failure
    recorded, because cert-manager waits for a missing CA to come back.
    """
    issuer = Issuer(name=issuer_name, namespace=namespace).load()
    secret_name = issuer["spec"]["ca"]["secretName"]
    data = read_secret(namespace, secret_name)
    broken = dict(data)
    broken["tls.key"] = _unrelated_private_key(data["tls.crt"])
    create_or_update_secret(namespace, secret_name, broken)
    return secret_name, data


def _unrelated_private_key(ca_cert_pem: str) -> str:
    """A new private key of the same type as the CA's, so signing fails on the key mismatch only."""
    public_key = x509.load_pem_x509_certificate(ca_cert_pem.encode()).public_key()
    if isinstance(public_key, rsa.RSAPublicKey):
        key = rsa.generate_private_key(public_exponent=65537, key_size=public_key.key_size)
    elif isinstance(public_key, ec.EllipticCurvePublicKey):
        key = ec.generate_private_key(public_key.curve)
    else:
        raise AssertionError(f"unsupported CA key type {type(public_key).__name__}")
    return key.private_bytes(
        serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()
    ).decode()


def restore_issuer_ca(namespace: str, secret_name: str, data: dict, issuer_name: str = "ca-issuer", timeout: int = 300):
    """Restores the issuer's CA Secret and waits until the Issuer can sign again."""
    create_or_update_secret(namespace, secret_name, data)
    run_periodically(
        lambda: bool(Issuer(name=issuer_name, namespace=namespace).is_ready()),
        timeout=timeout,
        sleep_time=3,
        msg=f"Issuer/{issuer_name} to be ready again",
    )
