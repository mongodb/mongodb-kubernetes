"""A TLS multi-cluster replica set with operator-managed certificates and SCRAM, where the members present
a clusterfile to each other even though internal cluster authentication is not x509.

The members authenticate each other with the keyfile, but with TLS on their connections are mutual
TLS, so each member still presents a client certificate. That is the clusterfile, signed by the
client CA, while the member certificate stays server-only. The operator issues both in the central
cluster and copies their -pem secrets and the client CA bundle into every member cluster. The
automation config sets net.tls.clusterFile and net.tls.clusterCAFile, but not clusterAuthMode.
"""

from typing import List

import kubernetes
from cryptography.x509.oid import ExtendedKeyUsageOID
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
from tests.tls.managedcert_helper import assert_clusterfile_without_x509, extended_key_usages

MDB_RESOURCE = "mc-rs-managedcert-clusterfile"
MEMBER_CLUSTER_MEMBERS = [2, 1, 2]


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    """Installs the operator with operator-managed certificates enabled, after cert-manager. In
    multi-cluster mode the kubectl-mongodb setup tool creates the operator RBAC, so the cert-manager
    RBAC is asked for there. This overrides the fixture of the same name in tests/conftest.py for this
    module only."""
    operator_installation_config = get_multi_cluster_operator_installation_config(namespace)
    operator_installation_config["security.managedCertificate.enabled"] = "true"
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
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, list(MEMBER_CLUSTER_MEMBERS))
    # No issuers, so the operator self-signs both the server CA and the client CA.
    resource["spec"]["security"] = {
        "managedCertificate": {},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@mark.e2e_managed_certificate_multi_cluster_rs_clusterfile_without_x509
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_rs_clusterfile_without_x509
def test_replica_set_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_managed_certificate_multi_cluster_rs_clusterfile_without_x509
def test_member_cert_is_server_only_and_clusterfile_is_client(namespace: str):
    member, clusterfile = f"{MDB_RESOURCE}-managed-cert", f"{MDB_RESOURCE}-managed-clusterfile"
    member_usages = extended_key_usages(namespace, member)
    assert ExtendedKeyUsageOID.SERVER_AUTH in member_usages, f"{member} does not allow serverAuth"
    assert ExtendedKeyUsageOID.CLIENT_AUTH not in member_usages, f"{member} should be server-only"
    assert ExtendedKeyUsageOID.CLIENT_AUTH in extended_key_usages(
        namespace, clusterfile
    ), f"{clusterfile} does not allow clientAuth"


@mark.e2e_managed_certificate_multi_cluster_rs_clusterfile_without_x509
def test_clusterfile_and_client_ca_are_in_every_member_cluster(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    # The pods run in the member clusters, so the clusterfile they present and the client CA they check
    # each other's clusterfile against have to be there too.
    for cluster_client in member_cluster_clients:
        pem = cluster_client.core_v1_api().read_namespaced_secret(f"{MDB_RESOURCE}-managed-clusterfile-pem", namespace)
        assert pem.data, f"clusterfile -pem missing/empty in member cluster {cluster_client.cluster_name}"
        bundle = cluster_client.read_namespaced_config_map(f"{MDB_RESOURCE}-managed-client-ca-bundle", namespace)
        assert (
            (bundle.data or {}).get("clusterca-pem", "").strip()
        ), f"client CA bundle missing/empty in member cluster {cluster_client.cluster_name}"


@mark.e2e_managed_certificate_multi_cluster_rs_clusterfile_without_x509
def test_automation_config_sets_the_clusterfile_without_x509(mongodb_multi: MongoDBMulti):
    assert_clusterfile_without_x509(
        mongodb_multi.get_automation_config_tester().automation_config, MDB_RESOURCE, sum(MEMBER_CLUSTER_MEMBERS)
    )
