"""Going back to the legacy certificates when a migration to operator-managed certificates doesn't finish.

The replica set starts the legacy way, the same as in tls_rs_managedcert_legacy_migration: TLS with the
user's certificates, signed by the test CA issuer (ca-issuer), and x509 for both the agent and internal
cluster authentication.

The migration is then started, and stopped part way: once the automation config names the managed
certificate files, but before the replica set reaches Running. This is the state a migration that fails
part way leaves behind, and the furthest a rollback has to recover from: the pods mount the managed -pem
secrets and the automation config names the managed files, including the managed client CA, which the
legacy pods don't mount. The customer then removes managedCertificate and puts tls.ca back, and the
replica set has to reach Running on the legacy certificates again.

Automatic recovery is turned off for this test, because it would rescue a stuck rollback after 20
minutes and hide the problem: the replica set has to reach Running on its own.
"""

import json

import pytest
from kubetester import read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_x509_agent_tls_certs, create_x509_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import run_periodically
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

MDB_RESOURCE = "test-tls-rs-managedcert-rollback"

# The annotation where the operator keeps the spec the resource last reached Running with.
LAST_ACHIEVED_SPEC_ANNOTATION = "mongodb.com/v1.lastSuccessfulConfiguration"


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


def migration_finished(mdb: MongoDB) -> bool:
    """Whether the resource has reached Running with managedCertificate, which means the migration finished."""
    mdb.load()
    last_achieved = json.loads(mdb["metadata"].get("annotations", {}).get(LAST_ACHIEVED_SPEC_ANNOTATION, "{}"))
    return "managedCertificate" in last_achieved.get("security", {})


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_rollback
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_rollback
def test_legacy_replica_set_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_rollback
def test_migration_is_stopped_part_way(mdb: MongoDB, namespace: str):
    mdb.load()
    # The same edit as in tls_rs_managedcert_legacy_migration. tls.ca and managedCertificate can't be set
    # together, so the same edit removes tls.ca; update() sends a merge patch, so None removes a field.
    mdb["spec"]["security"]["tls"]["ca"] = None
    membership = subject_from_certificate(namespace, f"{MDB_RESOURCE}-cert")
    agent = subject_from_certificate(namespace, "agent-certs")
    mdb["spec"]["security"]["managedCertificate"] = migration_managed_certificate(membership, agent)
    mdb.update()

    # Wait until the automation config names the managed client CA: from then on the pods mount the
    # managed certificates and the automation config names the managed files.
    def automation_config_names_the_managed_files() -> tuple[bool, str]:
        assert not migration_finished(mdb), "the migration finished before the test could stop it part way"
        tls = KubernetesTester.get_automation_config().get("tls") or {}
        return "clusterCAFilePath" in tls, f"tls={tls}"

    run_periodically(
        automation_config_names_the_managed_files,
        timeout=1200,
        sleep_time=2,
        msg="the automation config to name the managed certificate files",
    )


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_rollback
def test_rollback_to_the_legacy_certificates(mdb: MongoDB, issuer_ca_configmap: str):
    assert not migration_finished(mdb), "the migration finished before it was rolled back"

    mdb.load()
    mdb["spec"]["security"]["managedCertificate"] = None
    mdb["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    mdb.update()

    mdb.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_rollback
def test_pods_mount_the_legacy_certificates(mdb: MongoDB):
    sts = mdb.read_statefulset()
    mounted = {volume.secret.secret_name for volume in sts.spec.template.spec.volumes if volume.secret}
    for secret_name in [f"{MDB_RESOURCE}-cert", f"{MDB_RESOURCE}-clusterfile", "agent-certs"]:
        assert f"{secret_name}-pem" in mounted, f"{secret_name}-pem isn't mounted: {sorted(mounted)}"
    assert not any("-managed-" in name for name in mounted), f"a managed secret is still mounted: {sorted(mounted)}"


@pytest.mark.e2e_replica_set_tls_managedcert_legacy_migration_rollback
def test_automation_config_names_the_legacy_files(namespace: str):
    ac = KubernetesTester.get_automation_config()
    # The legacy pods don't mount the managed client CA, so its path must be gone.
    assert "clusterCAFilePath" not in ac["tls"], ac["tls"]
    assert ac["tls"]["CAFilePath"].endswith("/ca-pem"), ac["tls"]["CAFilePath"]
    assert_automation_config_names_files(
        agent_hash=read_secret(namespace, "agent-certs-pem")["latestHash"],
        member_hashes={MDB_RESOURCE: read_secret(namespace, f"{MDB_RESOURCE}-cert-pem")["latestHash"]},
        clusterfile_hashes={MDB_RESOURCE: read_secret(namespace, f"{MDB_RESOURCE}-clusterfile-pem")["latestHash"]},
        ac=ac,
    )
