"""Shared helpers for the operator-managed certificate (security.managedCertificate) replica set tests.

Each test file deploys the same replica set with a different managedCertificate block (which
category, if any, uses a user configured issuer), so the resource setup and the common checks
live here and the test files only keep what is specific to their scenario.
"""

from kubetester import read_configmap, read_secret, try_load
from kubetester.certs import Certificate
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import get_default_operator, get_operator_installation_config

# The user configured issuer the `issuer` fixture creates. It is a CA-type Issuer, so it writes its
# CA into each issued secret's ca.crt.
CA_ISSUER_REF = {"issuerRef": {"name": "ca-issuer", "kind": "Issuer"}}


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


def assert_leaf_secrets_created(name: str, namespace: str):
    """Every leaf certificate is issued into a kubernetes.io/tls secret, with no user created secret."""
    for secret_name in leaf_secret_names(name):
        secret = read_secret(namespace, secret_name)
        assert "tls.crt" in secret, f"{secret_name} missing tls.crt"
        assert "tls.key" in secret, f"{secret_name} missing tls.key"


def read_ca_bundles(name: str, namespace: str) -> tuple[str, str]:
    """Returns the server and client CA bundles the operator writes for the pods to trust.

    <name>-managed-server-ca-bundle (key ca-pem) is configured as net.tls.CAFile, and
    <name>-managed-client-ca-bundle (key clusterca-pem) as net.tls.clusterCAFile. Both must exist
    and be non-empty.
    """
    server_ca = read_configmap(namespace, f"{name}-managed-server-ca-bundle").get("ca-pem", "").strip()
    client_ca = read_configmap(namespace, f"{name}-managed-client-ca-bundle").get("clusterca-pem", "").strip()
    assert server_ca != "", "ca-pem (server CA) missing/empty"
    assert client_ca != "", "clusterca-pem (client CA) missing/empty"
    return server_ca, client_ca


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
