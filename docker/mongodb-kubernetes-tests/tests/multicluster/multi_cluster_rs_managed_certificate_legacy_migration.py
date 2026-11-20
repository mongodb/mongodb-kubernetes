"""Migrating a multi-cluster replica set from certificates the user created (legacy) to operator-managed
certificates.

The replica set starts the legacy way, TLS is on only through certsSecretPrefix, with the user's member,
clusterfile and agent certificates signed by the test CA issuer (ca-issuer), and x509 for both the agent
and internal cluster authentication. It is then migrated in one edit: certsSecretPrefix and tls.ca are
removed and managedCertificate is added, with the same issuer and the same subjects. This also covers a
legacy deployment whose TLS comes only from certsSecretPrefix. Automatic recovery is turned off, so the
replica set has to reach Running without it.
"""

from typing import List

import kubernetes
from kubetester import read_secret
from kubetester.certs_mongodb_multi import (
    create_multi_cluster_mongodb_x509_tls_certs,
    create_multi_cluster_x509_agent_certs,
)
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.tls.managedcert_helper import (
    RECOVERY_OFF_ENV_VAR,
    assert_automation_config_names_files,
    leaf_secret_names,
    migration_managed_certificate,
    subject_from_certificate,
)

MDB_RESOURCE = "mc-rs-managedcert-migration"
CERT_SECRET_PREFIX = "clustercert"

# The secrets a user creates for a legacy x509 multi-cluster replica set with certsSecretPrefix.
LEGACY_SECRETS = [
    f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert",
    f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-clusterfile",
    f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-agent-certs",
]


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    """Installs the operator with operator-managed certificates enabled, after cert-manager, and with
    automatic recovery turned off (see the module docstring). In multi-cluster mode the kubectl-mongodb
    setup tool creates the operator RBAC, so the cert-manager RBAC is asked for there. This overrides
    the fixture of the same name in tests/conftest.py for this module only."""
    operator_installation_config = get_multi_cluster_operator_installation_config(namespace)
    operator_installation_config["security.managedCertificate.enabled"] = "true"
    operator_installation_config["customEnvVars"] = operator_installation_config["customEnvVars"] + RECOVERY_OFF_ENV_VAR
    return get_multi_cluster_operator(
        namespace,
        get_central_cluster_name(),
        operator_installation_config,
        get_central_cluster_client(),
        get_member_cluster_clients(),
        get_member_cluster_names(),
        create_managed_certificate_roles=True,
    )


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names, custom_mdb_version: str) -> MongoDBMulti:
    # The cluster layout has to be set before the certificates are created: the certificate helpers
    # derive the names in the certificates from it.
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    return resource


@fixture(scope="module")
def legacy_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    for secret_name in LEGACY_SECRETS[:2]:
        create_multi_cluster_mongodb_x509_tls_certs(
            multi_cluster_issuer,
            secret_name,
            member_cluster_clients,
            central_cluster_client,
            mongodb_multi_unmarshalled,
        )
    create_multi_cluster_x509_agent_certs(
        multi_cluster_issuer,
        LEGACY_SECRETS[2],
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi_unmarshalled: MongoDBMulti,
    legacy_certs,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:
    resource = mongodb_multi_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {"ca": multi_cluster_issuer_ca_configmap},
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
            "internalCluster": "X509",
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@mark.e2e_managed_certificate_multi_cluster_rs_legacy_migration
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_rs_legacy_migration
def test_legacy_replica_set_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_managed_certificate_multi_cluster_rs_legacy_migration
def test_migrate_to_managed_certificate(
    mongodb_multi: MongoDBMulti, namespace: str, central_cluster_client: kubernetes.client.ApiClient
):
    mongodb_multi.load()
    # certsSecretPrefix and tls.ca can't be set together with managedCertificate, so the same edit
    # removes them. update() sends a merge patch, so a field is removed by setting it to None.
    security = mongodb_multi["spec"]["security"]
    security["certsSecretPrefix"] = None
    security["tls"]["ca"] = None
    # The subjects come from the legacy certificates, which the test created in the central cluster, so
    # the members keep recognising each other as peers while they restart one at a time.
    membership = subject_from_certificate(
        namespace, f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert", api_client=central_cluster_client
    )
    agent = subject_from_certificate(
        namespace, f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-agent-certs", api_client=central_cluster_client
    )
    security["managedCertificate"] = migration_managed_certificate(membership, agent)
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_managed_certificate_multi_cluster_rs_legacy_migration
def test_pods_mount_the_managed_certificates(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    expected = [f"{name}-pem" for name in leaf_secret_names(MDB_RESOURCE)]
    for cluster_name, sts in mongodb_multi.read_statefulsets(member_cluster_clients).items():
        mounted = {volume.secret.secret_name for volume in sts.spec.template.spec.volumes if volume.secret}
        for secret_name in expected:
            assert secret_name in mounted, f"{cluster_name}: {secret_name} isn't mounted: {sorted(mounted)}"


@mark.e2e_managed_certificate_multi_cluster_rs_legacy_migration
def test_automation_config_names_the_managed_certificates(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    # The pods mount the -pem secrets in their own member cluster. Every member cluster has to hold the
    # same files, so the hash is read from each of them and has to be the same everywhere.
    hashes = {}
    for name in leaf_secret_names(MDB_RESOURCE):
        per_cluster = {
            mcc.cluster_name: read_secret(namespace, f"{name}-pem", api_client=mcc.api_client)["latestHash"]
            for mcc in member_cluster_clients
        }
        assert len(set(per_cluster.values())) == 1, f"{name}-pem differs between member clusters: {per_cluster}"
        hashes[name] = next(iter(per_cluster.values()))

    member, clusterfile, agent = leaf_secret_names(MDB_RESOURCE)
    assert_automation_config_names_files(
        agent_hash=hashes[agent],
        member_hashes={MDB_RESOURCE: hashes[member]},
        clusterfile_hashes={MDB_RESOURCE: hashes[clusterfile]},
    )


@mark.e2e_managed_certificate_multi_cluster_rs_legacy_migration
def test_legacy_secrets_are_left_in_place(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    # The operator doesn't delete secrets it didn't create.
    for secret_name in LEGACY_SECRETS:
        secret = read_secret(namespace, secret_name, api_client=central_cluster_client)
        assert "tls.crt" in secret, f"{secret_name} is gone"
