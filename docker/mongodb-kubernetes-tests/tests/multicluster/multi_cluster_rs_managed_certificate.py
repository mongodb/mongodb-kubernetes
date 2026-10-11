from typing import List

import kubernetes
from cryptography import x509
from kubetester import read_configmap, read_secret
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE = "multi-cluster-replica-set"
MEMBER_CLUSTER_MEMBERS = [2, 2, 2]


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
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MEMBER_CLUSTER_MEMBERS)
    # Fully operator-managed certificates: the customer only enables the feature and picks X509
    # internal auth. No user-provided certs, no CA ConfigMap and no certsSecretPrefix anywhere.
    resource["spec"]["security"] = {
        "tls": {"enabled": True},
        "managedCertificate": {"enabled": True},
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
            "internalCluster": "X509",
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


def _dns_sans(cert_pem: str) -> List[str]:
    cert = x509.load_pem_x509_certificate(cert_pem.encode())
    ext = cert.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    return ext.value.get_values_for_type(x509.DNSName)


def _cert_revision(central_cluster_client: kubernetes.client.ApiClient, namespace: str, name: str) -> int:
    # cert-manager bumps status.revision by one every time it issues (or reissues) the certificate,
    # so it tells us exactly how many times the cert was issued.
    cert = kubernetes.client.CustomObjectsApi(central_cluster_client).get_namespaced_custom_object(
        group="cert-manager.io", version="v1", namespace=namespace, plural="certificates", name=name
    )
    return cert.get("status", {}).get("revision", 0)


@mark.e2e_managed_certificate_multi_cluster_rs
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_rs
def test_mongodb_multi_reaches_running(mongodb_multi: MongoDBMulti):
    # Reaching Running is the functional proof that every member in every member cluster presents a valid
    # managed server cert, authenticates with its managed clusterfile, and the agent authenticates
    # with the managed agent cert, with no user-provided certificates anywhere.
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_managed_certificate_multi_cluster_rs
def test_managed_leaf_and_ca_secrets_created_centrally(namespace: str):
    # The operator issues all leaf certs and the per-category (client/server) self-signed CAs in the central cluster.
    for name in (
        f"{MDB_RESOURCE}-cert",
        f"{MDB_RESOURCE}-clusterfile",
        f"{MDB_RESOURCE}-agent-certs",
        f"{MDB_RESOURCE}-server-ca",
        f"{MDB_RESOURCE}-client-ca",
    ):
        secret = read_secret(namespace, name)
        assert "tls.crt" in secret, f"{name} missing tls.crt"


@mark.e2e_managed_certificate_multi_cluster_rs
def test_member_cert_sans_cover_every_member_cluster(namespace: str):
    # There is one member certificate for the whole deployment, so its SANs are the union across all
    # member clusters. The SANs are the internal per-pod service names, and the cert must cover every
    # member of every cluster, so a missing member or a stray SAN fails the test.
    # This is the cert from the initial deployment, before the scale test below grows it (the cert
    # never shrinks, so the SANs stay at the peak once that test has run).
    member_crt = read_secret(namespace, f"{MDB_RESOURCE}-cert")["tls.crt"]
    sans = _dns_sans(member_crt)
    expected = sorted(
        f"{MDB_RESOURCE}-{cluster_index}-{pod_index}-svc.{namespace}.svc.cluster.local"
        for cluster_index, members in enumerate(MEMBER_CLUSTER_MEMBERS)
        for pod_index in range(members)
    )
    assert sorted(sans) == expected


@mark.e2e_managed_certificate_multi_cluster_rs
def test_ca_bundles_replicated_to_member_clusters(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    # The pods run in the member clusters and mount these ConfigMaps, but the operator only creates
    # them centrally. They must be copied into every member cluster, otherwise the pods have no CA to
    # trust and TLS/auth fails.
    for ca_bundle, key in (
        (f"{MDB_RESOURCE}-managed-server-ca-bundle", "ca-pem"),
        (f"{MDB_RESOURCE}-managed-client-ca-bundle", "clusterca-pem"),
    ):
        central = read_configmap(namespace, ca_bundle)
        assert central.get(key, "").strip() != "", f"{ca_bundle} ({key}) missing/empty on the central cluster"
        for cluster_client in member_cluster_clients:
            cm = cluster_client.read_namespaced_config_map(ca_bundle, namespace)
            assert (cm.data or {}).get(key, "").strip() != "", (
                f"{ca_bundle} ({key}) missing/empty in member cluster {cluster_client.cluster_name}"
            )


@mark.e2e_managed_certificate_multi_cluster_rs
def test_leaf_pem_secrets_present_in_every_member_cluster(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    # The operator derives a <secret>-pem secret from each managed leaf cert and the pods mount that.
    # It must exist in every member cluster for the pods to start with TLS.
    for base in (f"{MDB_RESOURCE}-cert", f"{MDB_RESOURCE}-clusterfile", f"{MDB_RESOURCE}-agent-certs"):
        for cluster_client in member_cluster_clients:
            secret = cluster_client.core_v1_api().read_namespaced_secret(f"{base}-pem", namespace)
            assert secret.data, f"{base}-pem missing/empty in member cluster {cluster_client.cluster_name}"


@mark.e2e_managed_certificate_multi_cluster_rs
def test_scale_up_and_down_members(
    mongodb_multi: MongoDBMulti,
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    # Scale one member cluster up by three members and then back down. The operator adds members one at
    # a time, so this exercises a multi-step scale. The member cert covers the union across clusters and
    # is grown to the target in one go, not reissued at every member step. Neither step should stall on
    # certificate churn.

    # Get the member certificate revision so that we can later check that certificate is not reissued
    # 3 times for 3 member scale up.
    member_cert = f"{MDB_RESOURCE}-cert"
    revision_before = _cert_revision(central_cluster_client, namespace, member_cert)
    cert_before = read_secret(namespace, member_cert)["tls.crt"]

    up = list(MEMBER_CLUSTER_MEMBERS)
    up[0] += 3
    mongodb_multi["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, up)
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)

    # The cert covers the target in one go, so the whole three-member scale up issues it exactly once.
    assert _cert_revision(central_cluster_client, namespace, member_cert) == revision_before + 1, (
        "member cert must be issued once for the whole scale up, not once per member step"
    )

    cert_after_up = read_secret(namespace, member_cert)["tls.crt"]
    assert cert_after_up != cert_before, "member cert must be reissued to cover the new members"

    down = list(MEMBER_CLUSTER_MEMBERS)
    mongodb_multi["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, down)
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)

    # Certificate is not changed at the scale down to have scaled down members, the SANs include all the
    # members that we had during scale up, so the cert is neither reissued nor changed.
    assert _cert_revision(central_cluster_client, namespace, member_cert) == revision_before + 1, (
        "member cert must not be reissued on scale down"
    )
    cert_after_down = read_secret(namespace, member_cert)["tls.crt"]
    assert cert_after_down == cert_after_up, "member cert must not shrink on scale down"
