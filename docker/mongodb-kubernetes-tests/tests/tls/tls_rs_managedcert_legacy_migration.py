"""Migrating a replica set from certificates the user created to operator-managed certificates.

The replica set starts the legacy way: TLS with the user's certificates in <name>-cert,
<name>-clusterfile and agent-certs, signed by the test CA issuer (ca-issuer), and x509 for both the
agent and internal cluster authentication, so the member, clusterfile and agent certificates all
change in the migration.

It is then migrated in one edit: tls.ca is removed and managedCertificate is added, pointing both
categories at the same issuer and keeping the subjects of the legacy certificates. With the same CA
and the same subjects, the members keep trusting each other and recognise each other as peers.

The pods mount new -pem secrets after the migration. The operator starts each of them with the
legacy certificate file in it, so a restarted pod still has the file the automation config names
until the config is updated, the same way it keeps the previous file during a rotation. Without
that, the agents can't load their certificate, mongod doesn't start and the reconcile gets stuck.
Automatic recovery is turned off for this test, because it would rescue a stuck migration after 20
minutes and hide the problem: the replica set has to reach Running on its own.
"""

import pytest
from kubetester import read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_x509_agent_tls_certs, create_x509_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import get_default_operator, get_operator_installation_config
from tests.tls.managedcert_helper import (
    RECOVERY_OFF_ENV_VAR,
    assert_automation_config_names_files,
    leaf_secret_names,
    migration_managed_certificate,
    subject_from_certificate,
)

MDB_RESOURCE = "test-tls-rs-managedcert-migration"

# The secrets a user creates for a legacy x509 replica set, without certsSecretPrefix.
LEGACY_SECRETS = [f"{MDB_RESOURCE}-cert", f"{MDB_RESOURCE}-clusterfile", "agent-certs"]


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert-manager has to be installed before the operator, which decides once, at startup, whether
    # it may use cert-manager. Automatic recovery is turned off, see the module docstring.
    config = get_operator_installation_config(namespace)
    config["security.managedCertificate.enabled"] = "true"
    config["customEnvVars"] = config["customEnvVars"] + RECOVERY_OFF_ENV_VAR
    return get_default_operator(namespace, config)


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


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration
def test_legacy_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration
def test_migrate_to_managed_certificate(mdb: MongoDB, namespace: str):
    mdb.load()
    # tls.ca and managedCertificate can't be set together, so the same edit removes tls.ca. update()
    # sends a merge patch, so a field is removed by setting it to None.
    mdb["spec"]["security"]["tls"]["ca"] = None
    # The subjects come from the certificates the pods use today, so the members keep recognising each
    # other as peers while they restart one at a time.
    membership = subject_from_certificate(namespace, f"{MDB_RESOURCE}-cert")
    agent = subject_from_certificate(namespace, "agent-certs")
    mdb["spec"]["security"]["managedCertificate"] = migration_managed_certificate(membership, agent)
    mdb.update()

    mdb.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration
def test_pods_mount_the_managed_certificates(mdb: MongoDB):
    sts = mdb.read_statefulset()
    mounted = {volume.secret.secret_name for volume in sts.spec.template.spec.volumes if volume.secret}
    for secret_name in leaf_secret_names(MDB_RESOURCE):
        assert f"{secret_name}-pem" in mounted, f"{secret_name}-pem isn't mounted: {sorted(mounted)}"


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration
def test_automation_config_names_the_managed_certificates(namespace: str):
    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    assert_automation_config_names_files(
        agent_hash=read_secret(namespace, f"{agent}-pem")["latestHash"],
        member_hashes={MDB_RESOURCE: read_secret(namespace, f"{member}-pem")["latestHash"]},
        clusterfile_hashes={MDB_RESOURCE: read_secret(namespace, f"{clusterfile}-pem")["latestHash"]},
    )


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration
def test_legacy_secrets_are_left_in_place(namespace: str):
    # The operator doesn't delete secrets it didn't create.
    for secret_name in LEGACY_SECRETS:
        assert "tls.crt" in read_secret(namespace, secret_name), f"{secret_name} is gone"
