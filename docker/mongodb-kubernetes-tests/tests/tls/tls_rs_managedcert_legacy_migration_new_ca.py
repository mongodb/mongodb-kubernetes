"""Migrating a replica set to operator-managed certificates signed by a different CA.

The replica set starts the legacy way: TLS with the user's certificates signed by the test CA issuer
(ca-issuer), and tls.ca pointing at that CA. It is then migrated to managed certificates signed by a
*different, unrelated* CA: nothing chains the certificates it signs to the CA the deployment trusted,
so the members only accept them because the trust bundle carries both CAs.

That is what the trust bundle has to do. While the pods restart one at a time, the members that have
not restarted yet present certificates signed by the CA the deployment had, and the ones that have
present certificates signed by the new CA, so each side has to trust the other's. The operator refuses
the migration when the old CA would not be in the bundle, which is what
spec.security.managedCertificate.<category>.additionalTrustedCAs is for; this test sets it and checks
the migration goes through.

The first rollout with a different CA is slower than the same-CA migration: a member that has restarted
presents a certificate the members that have not restarted yet cannot validate, so it joins only once
enough of them carry the new bundle. The deployment stays available throughout, but the operator's wait
for the agents can expire on the way, which it reports as a Failed phase before recovering.
"""

import pytest
from kubetester import read_configmap, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_x509_agent_tls_certs, create_x509_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import get_default_operator, get_operator_installation_config
from tests.tls.managedcert_helper import (
    RECOVERY_OFF_ENV_VAR,
    assert_automation_config_names_files,
    assert_reachable_with_client_certificate,
    client_certificate_user,
    leaf_secret_names,
    subject_from_certificate,
)

MDB_RESOURCE = "test-tls-rs-managedcert-migration-new-ca"

# The secrets a user creates for a legacy x509 replica set, without certsSecretPrefix.
LEGACY_SECRETS = [f"{MDB_RESOURCE}-cert", f"{MDB_RESOURCE}-clusterfile", "agent-certs"]


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert-manager has to be installed before the operator, which decides once, at startup, whether
    # it may use cert-manager. Automatic recovery is turned off, so a stuck migration fails the test
    # instead of being rescued after 20 minutes.
    config = get_operator_installation_config(namespace)
    config["security.managedCertificate.enabled"] = "true"
    config["customEnvVars"] = config["customEnvVars"] + RECOVERY_OFF_ENV_VAR
    return get_default_operator(namespace, config)


@pytest.fixture(scope="module")
def other_ca_issuer(cert_manager: str, namespace: str) -> str:
    """An issuer whose CA is not the CA the legacy deployment trusts.

    The CA is self-signed rather than signed by the test CA, so the certificates it issues do not chain
    to the CA the deployment had: the members accept them only when both CAs are in the trust bundle.
    """
    from kubetester.certs import Certificate, Issuer

    selfsigned = Issuer(name="other-ca-selfsigned", namespace=namespace)
    selfsigned["spec"] = {"selfSigned": {}}
    # No block_until_ready: the Issuer class waits for the KeyPairVerified condition, which
    # cert-manager only sets on CA issuers, not on a self-signed one.
    selfsigned.create()

    ca = Certificate(namespace=namespace, name="other-ca")
    ca["spec"] = {
        "isCA": True,
        "commonName": "other-ca",
        "secretName": "other-ca-secret",
        "issuerRef": {"name": "other-ca-selfsigned"},
        "dnsNames": ["other-ca.example.com"],
    }
    ca.create().block_until_ready()

    issuer = Issuer(name="other-ca-issuer", namespace=namespace)
    issuer["spec"] = {"ca": {"secretName": "other-ca-secret"}}
    issuer.create().block_until_ready()
    return "other-ca-issuer"


@pytest.fixture(scope="module")
def legacy_certs(issuer: str, namespace: str):
    create_x509_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")
    create_x509_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-clusterfile")
    create_x509_agent_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def mdb(namespace: str, legacy_certs, issuer_ca_configmap: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-x509-all-options-rs.yaml"), name=MDB_RESOURCE, namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_legacy_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_migrate_to_managed_certificates_from_a_different_ca(
    mdb: MongoDB, namespace: str, issuer_ca_configmap: str, other_ca_issuer: str
):
    mdb.load()
    # tls.ca and managedCertificate can't be set together, so the same edit removes tls.ca. update()
    # sends a merge patch, so a field is removed by setting it to None.
    mdb["spec"]["security"]["tls"]["ca"] = None
    # The subjects come from the certificates the pods use today, so the members keep recognising each
    # other as peers while they restart one at a time.
    membership = subject_from_certificate(namespace, f"{MDB_RESOURCE}-cert")
    agent = subject_from_certificate(namespace, "agent-certs")
    # The CA the deployment trusts today stays trusted, in both categories: the members validate each
    # other's certificates against the server bundle and their clusterfiles against the client bundle.
    legacy_ca = {"name": issuer_ca_configmap, "kind": "ConfigMap", "key": "ca-pem"}
    mdb["spec"]["security"]["managedCertificate"] = {
        "server": {
            "issuerRef": {"name": other_ca_issuer, "kind": "Issuer"},
            "additionalTrustedCAs": [legacy_ca],
        },
        "client": {
            "issuerRef": {"name": other_ca_issuer, "kind": "Issuer"},
            "additionalTrustedCAs": [legacy_ca],
        },
        "subjects": {"membership": membership, "agent": agent},
    }
    mdb.update()

    # ignore_errors: with a different CA the first rollout takes longer than the operator waits for the
    # agents. A member that has restarted presents a certificate the members that have not restarted yet
    # cannot validate, because a running mongod read the CA file before the bundle was widened, so it
    # only joins once enough members carry the new bundle. The operator reports the wait expiring as a
    # Failed phase and recovers on a later reconcile; a migration that never converges still fails the
    # test on the timeout.
    mdb.assert_reaches_phase(Phase.Running, timeout=1200, ignore_errors=True)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_trust_bundles_keep_the_ca_the_deployment_had(namespace: str, issuer_ca_configmap: str):
    # Both bundles carry the CA the deployment trusted before the migration, next to the one that
    # signs the managed certificates now.
    legacy_ca = read_configmap(namespace, issuer_ca_configmap)["ca-pem"].strip()

    for bundle in (
        f"{MDB_RESOURCE}-managed-server-ca-bundle",
        f"{MDB_RESOURCE}-managed-client-ca-bundle",
    ):
        published = " ".join(read_configmap(namespace, bundle).values())
        assert legacy_ca in published, f"{bundle} has to keep the CA the deployment trusted before the migration"


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_pods_mount_the_managed_certificates(mdb: MongoDB):
    sts = mdb.read_statefulset()
    mounted = {volume.secret.secret_name for volume in sts.spec.template.spec.volumes if volume.secret}
    for secret_name in leaf_secret_names(MDB_RESOURCE):
        assert f"{secret_name}-pem" in mounted, f"{secret_name}-pem isn't mounted: {sorted(mounted)}"


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_automation_config_names_the_managed_certificates(namespace: str):
    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    assert_automation_config_names_files(
        agent_hash=read_secret(namespace, f"{agent}-pem")["latestHash"],
        member_hashes={MDB_RESOURCE: read_secret(namespace, f"{member}-pem")["latestHash"]},
        clusterfile_hashes={MDB_RESOURCE: read_secret(namespace, f"{clusterfile}-pem")["latestHash"]},
    )


@pytest.fixture(scope="module")
def app_user(mdb: MongoDB, namespace: str) -> MongoDBUser:
    """The user an application connects as, with an operator-issued client certificate."""
    return client_certificate_user(mdb, namespace)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_can_connect_with_a_client_certificate_after_the_migration(mdb: MongoDB, app_user: MongoDBUser, namespace: str):
    # The application's certificate is issued by the client CA of the migration, the new one, and it
    # verifies the server against the managed server bundle, which carries both CAs. Connecting is what
    # proves the deployment serves certificates its clients accept after the CA change.
    assert_reachable_with_client_certificate(mdb, app_user, namespace)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_new_ca
def test_legacy_secrets_are_left_in_place(namespace: str):
    # The operator doesn't delete secrets it didn't create, so the CA the deployment had stays there
    # for as long as the customer keeps it.
    for secret_name in LEGACY_SECRETS:
        assert "tls.crt" in read_secret(namespace, secret_name), f"{secret_name} is gone"
