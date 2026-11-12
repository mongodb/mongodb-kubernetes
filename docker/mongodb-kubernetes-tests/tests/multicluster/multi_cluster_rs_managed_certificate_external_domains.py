"""A multi-cluster replica set with operator-managed certificates where every cluster has its own external domain.

With an external domain the members of that cluster are registered and addressed as
<pod>.<cluster's external domain>, so that is the name a client connects to and the name the certificate
has to be usable for. One member certificate covers every cluster, so its SANs are the union: the
internal service name of each member plus the external name each member is reached by, in that member's
own cluster's domain.

The domains differ per cluster, which a single wildcard could not cover, so this also covers that the
certificate lists the real hostnames instead.
"""

from typing import List

import kubernetes
from cryptography import x509
from kubetester import read_secret, read_service, try_load, wait_until
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
    update_coredns_hosts,
)
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE = "multi-cluster-rs-managedcert-external-domains"
MEMBER_CLUSTER_MEMBERS = [1, 1, 1]


def external_domain(cluster_name: str) -> str:
    """The external domain of one member cluster. Each cluster has its own, and the operator addresses
    the members of that cluster under it."""
    return f"{cluster_name}.interconnected"


def external_hostname(cluster_index: int, cluster_name: str) -> str:
    return f"{MDB_RESOURCE}-{cluster_index}-0.{external_domain(cluster_name)}"


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
        "tls": {"enabled": True},
        "managedCertificate": {},
        "authentication": {"enabled": True, "modes": ["X509"], "internalCluster": "X509"},
    }
    for item in resource["spec"]["clusterSpecList"]:
        item["externalAccess"] = {"externalDomain": external_domain(item["clusterName"])}
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.e2e_managed_certificate_multi_cluster_rs_external_domains
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_rs_external_domains
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    # The members cannot reach each other until their external hostnames resolve, which the coredns
    # entries below take care of, so this only creates the resource and the external services.
    mongodb_multi.update()


@mark.e2e_managed_certificate_multi_cluster_rs_external_domains
def test_update_coredns(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    # Each cluster resolves its own member's external hostname to that member's LoadBalancer address.
    for cluster_index, cluster_client in enumerate(member_cluster_clients):
        hostname = external_hostname(cluster_index, cluster_client.cluster_name)
        service_name = f"{MDB_RESOURCE}-{cluster_index}-0-svc-external"

        # The LoadBalancer address is assigned shortly after the operator creates the service.
        wait_until(lambda: bool(_load_balancer_address(namespace, service_name, cluster_client)), timeout=300)

        update_coredns_hosts(
            [(_load_balancer_address(namespace, service_name, cluster_client), hostname)],
            cluster_client.cluster_name,
            api_client=cluster_client.api_client,
        )


@mark.e2e_managed_certificate_multi_cluster_rs_external_domains
def test_mongodb_multi_reaches_running(mongodb_multi: MongoDBMulti):
    # Reaching Running proves the members resolve and connect to each other over their own cluster's
    # external domain, with the managed certificates.
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_managed_certificate_multi_cluster_rs_external_domains
def test_member_certificate_covers_every_cluster_domain(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    # The certificate is asserted as a whole: the internal service name of every member, plus the
    # external name that member is reached by in its own cluster's domain. One wildcard could not
    # cover domains that differ per cluster, so a missing name or a wildcard fails the test.
    sans = _dns_sans(read_secret(namespace, f"{MDB_RESOURCE}-managed-cert", central_cluster_client)["tls.crt"])

    expected = [f"{MDB_RESOURCE}-{i}-0-svc.{namespace}.svc.cluster.local" for i in range(len(MEMBER_CLUSTER_MEMBERS))]
    expected += [
        external_hostname(i, cluster_client.cluster_name) for i, cluster_client in enumerate(member_cluster_clients)
    ]

    assert sorted(sans) == sorted(expected)


@mark.e2e_managed_certificate_multi_cluster_rs_external_domains
def test_members_are_addressed_by_their_cluster_domain(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    # The certificate is built from the spec, so on its own it would still carry the external hostnames
    # if the operator did not address the members by them. This checks the two agree.
    ac = mongodb_multi.get_automation_config_tester().automation_config
    replica_set = next(rs for rs in ac["replicaSets"] if rs["_id"] == MDB_RESOURCE)
    processes = [
        process for process in ac["processes"] if process["name"] in [m["host"] for m in replica_set["members"]]
    ]

    assert sorted(process["hostname"] for process in processes) == sorted(
        external_hostname(i, cluster_client.cluster_name) for i, cluster_client in enumerate(member_cluster_clients)
    )


def _load_balancer_address(namespace: str, service_name: str, cluster_client: MultiClusterClient) -> str:
    service = read_service(namespace, service_name, api_client=cluster_client.api_client)
    addresses = service.status.load_balancer.ingress
    if not addresses:
        return ""
    return addresses[0].ip or addresses[0].hostname


def _dns_sans(cert_pem: str) -> list[str]:
    """The DNS names of a certificate, empty when it carries no hostnames at all."""
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    try:
        extension = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    except x509.ExtensionNotFound:
        return []
    return extension.value.get_values_for_type(x509.DNSName)
