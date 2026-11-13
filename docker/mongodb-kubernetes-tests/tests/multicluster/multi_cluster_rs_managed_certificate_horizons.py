"""A multi-cluster replica set with operator-managed certificates, split horizon and additional certificate domains.

The member certificate is the one clients see, so it has to carry every name the deployment is reached
by. Besides the internal service names and the per-cluster external domains, that is the hostname of
each member's replica set horizon and any additional certificate domain the customer configured.

Both were missing from the multi-cluster member certificate: the options the certificate is issued from
did not carry them, so clients connecting through a horizon hostname failed the TLS hostname check even
though the deployment worked.
"""

from typing import List

import kubernetes
from cryptography import x509
from kubetester import read_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
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

MDB_RESOURCE = "multi-cluster-rs-managedcert-horizons"
MEMBER_CLUSTER_MEMBERS = [1, 1, 1]

# One horizon hostname per member, in the order the members are created: cluster by cluster, and pod by
# pod inside a cluster. The ports are not part of a certificate name.
HORIZON_HOSTNAMES = [
    "mdb-0-0.example.com",
    "mdb-1-0.example.com",
    "mdb-2-0.example.com",
]
ADDITIONAL_DOMAIN = "additional.example.org"


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    """Installs the operator with operator-managed certificates enabled, after cert-manager.

    The operator decides once, at startup, whether it may use cert-manager, so cert-manager has to be
    installed first (the cert_manager dependency) and the operator needs the cert-manager RBAC, which
    in multi-cluster mode the kubectl-mongodb setup tool creates.
    """
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
    cert_manager: str,
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, list(MEMBER_CLUSTER_MEMBERS))
    # No issuer for either category: the operator self-signs both the server and the client CAs.
    resource["spec"]["security"] = {
        "tls": {"enabled": True, "additionalCertificateDomains": [ADDITIONAL_DOMAIN]},
        "managedCertificate": {},
        "authentication": {"enabled": True, "modes": ["X509"], "internalCluster": "X509"},
    }
    resource["spec"]["connectivity"] = {
        "replicaSetHorizons": [{"external": f"{hostname}:27017"} for hostname in HORIZON_HOSTNAMES],
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.e2e_managed_certificate_multi_cluster_rs_horizons
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_rs_horizons
def test_mongodb_multi_reaches_running(mongodb_multi: MongoDBMulti):
    # Reaching Running proves the operator accepts the horizons and the additional domains, and that
    # the members authenticate each other with the managed certificates.
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_managed_certificate_multi_cluster_rs_horizons
def test_member_certificate_covers_the_horizons_and_the_additional_domains(
    namespace: str, central_cluster_client: kubernetes.client.ApiClient
):
    # The member certificate is the one clients see, so it carries every name the deployment is
    # reached by: the internal service name of every member, that member's horizon hostname, and the
    # additional certificate domain. The list is asserted as a whole rather than only the names this
    # change added, so a member that is missing or a name that should not be there fails too. A
    # horizon hostname carries a port in the spec, and a certificate name has none.
    sans = _dns_sans(read_secret(namespace, f"{MDB_RESOURCE}-managed-cert", central_cluster_client)["tls.crt"])

    members = range(len(MEMBER_CLUSTER_MEMBERS))
    expected = [f"{MDB_RESOURCE}-{i}-0-svc.{namespace}.svc.cluster.local" for i in members]
    expected += HORIZON_HOSTNAMES
    expected += [f"{MDB_RESOURCE}-{i}-0.{ADDITIONAL_DOMAIN}" for i in members]

    assert sorted(sans) == sorted(expected)


@mark.e2e_managed_certificate_multi_cluster_rs_horizons
def test_the_members_are_configured_with_the_horizons(mongodb_multi: MongoDBMulti):
    # The certificate is built from the spec, so on its own it would still carry the horizon hostnames
    # if the operator dropped the horizons from the deployment. This checks the two agree.
    ac = mongodb_multi.get_automation_config_tester().automation_config
    replica_set = next(rs for rs in ac["replicaSets"] if rs["_id"] == MDB_RESOURCE)

    configured = sorted(
        hostname for member in replica_set["members"] for hostname in member.get("horizons", {}).values()
    )
    assert configured == sorted(f"{hostname}:27017" for hostname in HORIZON_HOSTNAMES)


def _dns_sans(cert_pem: str) -> list[str]:
    """The DNS names of a certificate, empty when it carries no hostnames at all."""
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    try:
        extension = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    except x509.ExtensionNotFound:
        return []
    return extension.value.get_values_for_type(x509.DNSName)
