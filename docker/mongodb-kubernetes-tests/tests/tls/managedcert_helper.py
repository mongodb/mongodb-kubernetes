"""Shared helpers for the operator-managed certificate (security.managedCertificate) replica set tests.

Each test file deploys the same replica set with a different managedCertificate block (which
category, if any, uses a user configured issuer), so the resource setup and the common checks
live here and the test files only keep what is specific to their scenario.
"""

import tempfile
from typing import Any, List, Optional

from cryptography import x509
from cryptography.x509.oid import NameOID
from kubetester import read_configmap, read_secret, try_load
from kubetester.certs import Certificate
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
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
    mdb: MongoDB, user: MongoDBUser, namespace: str, service_names: Optional[List[str]] = None
):
    """Connects as the user with its operator-issued certificate, the way an application would.

    The user's status names the two things an application needs: the Secret with its certificate and
    key, and the ConfigMap with the CA that verifies the server. mongod requires a client certificate
    because x509 is the only authentication mode, and checks it against the client CA.

    service_names is only needed for a multi-cluster sharded deployment, which has no single mongos
    service: the caller passes the service of each mongos pod.
    """
    user.load()
    cert_status = user["status"]["clientCertificate"]
    cert = read_secret(namespace, cert_status["secretName"])
    server_ca = read_configmap(namespace, cert_status["serverCAConfigMapName"])["ca-pem"]

    with (
        tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as cert_file,
        tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as ca_file,
    ):
        # Drivers take the certificate and key as one PEM file.
        cert_file.write(cert["tls.crt"] + cert["tls.key"])
        ca_file.write(server_ca)

    mdb.tester(use_ssl=True, ca_path=ca_file.name, service_names=service_names).assert_connectivity(
        opts=[with_x509(cert_file.name, ca_file.name)]
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
