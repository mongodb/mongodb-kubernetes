"""Covers the --member-cluster-ca flag of `kubectl mongodb multicluster setup`.

The e2e clusters have no TLS terminator, so the override used here is the member cluster's own CA
with an unrelated self signed CA appended: byte-wise different from what the plugin writes on its
own, which is what makes the override observable, while still chaining to the API server's cert.
"""

import base64
import tempfile

import kubernetes
import yaml
from kubetester import (
    delete_statefulset,
    get_statefulset,
    read_secret,
    wait_for_statefulset_ready,
    wait_for_statefulset_recreated,
    wait_until,
)
from kubetester.crypto import generate_self_signed_ca_pem
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import run_kube_config_creation_tool
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"
# The Operator's KubeConfig secret keeps its pre-MCK name, see common.KubeConfigSecretName.
KUBECONFIG_SECRET_NAME = "mongodb-enterprise-operator-multi-cluster-kubeconfig"
SERVICE_ACCOUNT_NAME = "mongodb-kubernetes-operator-multi-cluster"


def read_kubeconfig_cas(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> dict[str, str]:
    """Returns the certificate-authority-data of every cluster in the Operator's KubeConfig, as PEM."""
    secret = read_secret(namespace, KUBECONFIG_SECRET_NAME, api_client=central_cluster_client)
    kubeconfig = yaml.safe_load(secret["kubeconfig"])
    return {
        entry["name"]: base64.b64decode(entry["cluster"]["certificate-authority-data"]).decode("utf-8")
        for entry in kubeconfig["clusters"]
    }


def read_service_account_ca(namespace: str, api_client: kubernetes.client.ApiClient) -> str:
    """Returns ca.crt from the Operator ServiceAccount's token secret on a member cluster."""
    secrets = kubernetes.client.CoreV1Api(api_client=api_client).list_namespaced_secret(namespace).items
    for secret in secrets:
        if secret.metadata.name.startswith(f"{SERVICE_ACCOUNT_NAME}-token"):
            return base64.b64decode(secret.data["ca.crt"]).decode("utf-8")

    raise AssertionError(f"no token secret for service account {SERVICE_ACCOUNT_NAME} in namespace {namespace}")


@fixture(scope="module")
def custom_ca_cluster(member_cluster_names: list[str]) -> str:
    """The single member cluster whose CA is overridden. The others are the control group."""
    return member_cluster_names[0]


@fixture(scope="module")
def custom_ca_bundle(
    custom_ca_cluster: str,
    namespace: str,
    member_cluster_clients: list[MultiClusterClient],
) -> str:
    member_cluster_client = next(c for c in member_cluster_clients if c.cluster_name == custom_ca_cluster)
    cluster_ca = read_service_account_ca(namespace, member_cluster_client.api_client)
    unrelated_ca = generate_self_signed_ca_pem(f"{custom_ca_cluster}-external-terminator").decode("utf-8")

    return cluster_ca.rstrip("\n") + "\n" + unrelated_ca


@fixture(scope="module")
def custom_ca_file(custom_ca_bundle: str) -> str:
    with tempfile.NamedTemporaryFile(delete=False, mode="w", suffix=".pem") as ca_file:
        ca_file.write(custom_ca_bundle)
    return ca_file.name


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource


@mark.e2e_multi_cluster_cli_custom_ca
def test_setup_without_custom_ca_keeps_service_account_ca(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    member_cluster_clients: list[MultiClusterClient],
):
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)

    kubeconfig_cas = read_kubeconfig_cas(namespace, central_cluster_client)
    assert sorted(kubeconfig_cas) == sorted(member_cluster_names)

    for member_cluster_client in member_cluster_clients:
        assert kubeconfig_cas[member_cluster_client.cluster_name] == read_service_account_ca(
            namespace, member_cluster_client.api_client
        ), f"without --member-cluster-ca, {member_cluster_client.cluster_name} should keep its ServiceAccount CA"


@mark.e2e_multi_cluster_cli_custom_ca
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_multi_cluster_cli_custom_ca
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_multi_cluster_cli_custom_ca
def test_setup_with_custom_ca_overrides_only_the_target_cluster(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    member_cluster_clients: list[MultiClusterClient],
    custom_ca_cluster: str,
    custom_ca_bundle: str,
    custom_ca_file: str,
):
    run_kube_config_creation_tool(
        member_cluster_names,
        namespace,
        namespace,
        member_cluster_names,
        member_cluster_cas={custom_ca_cluster: custom_ca_file},
    )

    kubeconfig_cas = read_kubeconfig_cas(namespace, central_cluster_client)
    assert (
        kubeconfig_cas[custom_ca_cluster] == custom_ca_bundle
    ), f"{custom_ca_cluster} should carry the supplied PEM bundle verbatim"

    for member_cluster_client in member_cluster_clients:
        if member_cluster_client.cluster_name == custom_ca_cluster:
            continue
        assert kubeconfig_cas[member_cluster_client.cluster_name] == read_service_account_ca(
            namespace, member_cluster_client.api_client
        ), f"{member_cluster_client.cluster_name} was not overridden and should keep its ServiceAccount CA"


@mark.e2e_multi_cluster_cli_custom_ca
def test_operator_reconciles_member_cluster_with_custom_ca(
    namespace: str,
    multi_cluster_operator: Operator,
    mongodb_multi: MongoDBMulti,
    member_cluster_names: list[str],
    member_cluster_clients: list[MultiClusterClient],
    custom_ca_cluster: str,
):
    # the Operator builds its member cluster clients at startup, so the override lands only on restart
    multi_cluster_operator.restart_operator_deployment()

    # deleting the StatefulSet forces a read and a write through the client that trusts the custom CA
    member_cluster_client = next(c for c in member_cluster_clients if c.cluster_name == custom_ca_cluster)
    sts_name = f"{mongodb_multi.name}-{member_cluster_names.index(custom_ca_cluster)}"
    old_uid = get_statefulset(namespace, sts_name, api_client=member_cluster_client.api_client).metadata.uid

    delete_statefulset(namespace=namespace, name=sts_name, api_client=member_cluster_client.api_client)
    wait_for_statefulset_recreated(namespace, sts_name, old_uid, api_client=member_cluster_client.api_client)
    wait_for_statefulset_ready(namespace, sts_name, api_client=member_cluster_client.api_client, timeout=800)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_multi_cluster_cli_custom_ca
def test_member_clusters_report_healthy_with_custom_ca(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_operator: Operator,
    mongodb_multi: MongoDBMulti,
    member_cluster_names: list[str],
):
    # a bad CA fails here quietly: reconciliation keeps working while every cluster is marked failed
    def all_clusters_reported_healthy() -> bool:
        operator_pod = multi_cluster_operator.list_operator_pods()[0]
        logs = KubernetesTester.read_pod_logs(namespace, operator_pod.metadata.name, api_client=central_cluster_client)
        return all(f"Cluster {cluster_name} reported healthy" in logs for cluster_name in member_cluster_names)

    wait_until(all_clusters_reported_healthy, timeout=300)

    mongodb_multi.load()
    assert "failedClusters" not in (mongodb_multi["metadata"].get("annotations") or {})
