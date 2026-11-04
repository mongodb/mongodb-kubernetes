"""Migrating a sharded cluster from certificates the user created to operator-managed certificates.

The same migration as tls_rs_managedcert_legacy_migration.py, for the sharded cluster controller.
It matters most here: the components restart one after the other, and mongos can't work while the
config servers are down. If the restarted config servers lost the certificate files the automation
config names, mongos would stop being ready, its StatefulSet could not roll, and the operator would
never get to update the automation config. Keeping the legacy files in the new -pem secrets keeps
every component running through the migration.

The cluster starts the legacy way: TLS with the user's certificates (a member and a clusterfile
certificate per component, and agent-certs), signed by the test CA issuer (ca-issuer), and x509 for
both the agent and internal cluster authentication. It is then migrated in one edit: tls.ca is
removed and managedCertificate is added, with the same issuer and the same subjects. Automatic
recovery is turned off, so the cluster has to reach Running without it.
"""

import pytest
from kubernetes import client
from kubetester import find_fixture, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_sharded_cluster_certs, create_x509_agent_tls_certs
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import get_default_operator, get_operator_installation_config
from tests.tls.managedcert_helper import (
    RECOVERY_OFF_ENV_VAR,
    assert_automation_config_names_files,
    migration_managed_certificate,
    subject_from_certificate,
)

MDB_RESOURCE = "test-tls-sc-managedcert-migration"

# Every component runs in its own StatefulSet and has its own member and clusterfile certificates.
COMPONENTS = [f"{MDB_RESOURCE}-0", f"{MDB_RESOURCE}-config", f"{MDB_RESOURCE}-mongos"]

# The secrets a user creates for a legacy x509 sharded cluster, without certsSecretPrefix.
LEGACY_SECRETS = [f"{c}-{kind}" for c in COMPONENTS for kind in ("cert", "clusterfile")] + ["agent-certs"]


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
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=3,
        internal_auth=True,
        x509_certs=True,
    )
    create_x509_agent_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def sc(namespace: str, legacy_certs, issuer_ca_configmap: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("test-x509-all-options-sc.yaml"), name=MDB_RESOURCE, namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    resource.set_architecture_annotation()
    try_load(resource)
    return resource


@pytest.mark.e2e_sharded_cluster_tls_managedcert_legacy_migration
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_sharded_cluster_tls_managedcert_legacy_migration
def test_legacy_sharded_cluster_running(sc: MongoDB):
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_legacy_migration
def test_migrate_to_managed_certificate(sc: MongoDB, namespace: str):
    sc.load()
    # tls.ca and managedCertificate can't be set together, so the same edit removes tls.ca. update()
    # sends a merge patch, so a field is removed by setting it to None.
    sc["spec"]["security"]["tls"]["ca"] = None
    # The subjects come from the certificates the pods use today (every component carries the same
    # membership subject), so the components keep recognising each other as peers while they restart.
    membership = subject_from_certificate(namespace, f"{MDB_RESOURCE}-0-cert")
    agent = subject_from_certificate(namespace, "agent-certs")
    sc["spec"]["security"]["managedCertificate"] = migration_managed_certificate(membership, agent)
    sc.update()

    # Every component restarts, so this takes longer than for a replica set.
    sc.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_legacy_migration
def test_pods_mount_the_managed_certificates(namespace: str):
    apps = client.AppsV1Api()
    for component in COMPONENTS:
        sts = apps.read_namespaced_stateful_set(component, namespace)
        mounted = {volume.secret.secret_name for volume in sts.spec.template.spec.volumes if volume.secret}
        expected = [
            f"{component}-managed-cert-pem",
            f"{component}-managed-clusterfile-pem",
            f"{MDB_RESOURCE}-managed-agent-certs-pem",
        ]
        for secret_name in expected:
            assert secret_name in mounted, f"{component}: {secret_name} isn't mounted: {sorted(mounted)}"


@pytest.mark.e2e_sharded_cluster_tls_managedcert_legacy_migration
def test_automation_config_names_the_managed_certificates(namespace: str):
    assert_automation_config_names_files(
        agent_hash=read_secret(namespace, f"{MDB_RESOURCE}-managed-agent-certs-pem")["latestHash"],
        member_hashes={c: read_secret(namespace, f"{c}-managed-cert-pem")["latestHash"] for c in COMPONENTS},
        clusterfile_hashes={
            c: read_secret(namespace, f"{c}-managed-clusterfile-pem")["latestHash"] for c in COMPONENTS
        },
    )


@pytest.mark.e2e_sharded_cluster_tls_managedcert_legacy_migration
def test_legacy_secrets_are_left_in_place(namespace: str):
    # The operator doesn't delete secrets it didn't create.
    for secret_name in LEGACY_SECRETS:
        assert "tls.crt" in read_secret(namespace, secret_name), f"{secret_name} is gone"
