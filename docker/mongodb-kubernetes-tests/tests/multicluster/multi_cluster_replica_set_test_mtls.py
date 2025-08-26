from typing import List

import kubernetes
import pytest
from kubetester import wait_until
from kubetester.kubetester import KubernetesTester, create_testing_namespace
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace)
    resource.set_version(custom_mdb_version)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    # TODO: incorporate this into the base class.
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@pytest.mark.e2e_multi_cluster_mtls_test
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_mtls_test
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1400)


@pytest.mark.e2e_multi_cluster_mtls_test
def test_create_mongo_pod_in_separate_namespace(
    member_cluster_clients: List[MultiClusterClient],
    evergreen_task_id: str,
    namespace: str,
):
    cluster_1_client = member_cluster_clients[0]

    # create the namespace to deploy the
    create_testing_namespace(evergreen_task_id, f"{namespace}-mongo", api_client=cluster_1_client.api_client)

    corev1 = kubernetes.client.CoreV1Api(api_client=cluster_1_client.api_client)

    # def default_service_account_token_exists() -> bool:
    #     secrets: kubernetes.client.V1SecretList = corev1.list_namespaced_secret(
    #         f"{namespace}-mongo"
    #     )
    #     for secret in secrets.items:
    #         if secret.metadata.name.startswith("default-token"):
    #             return True
    #     return False
    #
    # wait_until(default_service_account_token_exists, timeout=10)

    # create a pod with mongo installed in a separate namespace that does not have istio configured.
    corev1.create_namespaced_pod(
        f"{namespace}-mongo",
        {
            "apiVersion": "v1",
            "kind": "Pod",
            "metadata": {
                "name": "mongo",
            },
            "spec": {
                "containers": [
                    {
                        "image": "mongo",
                        "name": "mongo",
                    }
                ],
                "dnsPolicy": "ClusterFirst",
                "restartPolicy": "Never",
            },
        },
    )

    def pod_is_ready() -> bool:
        try:
            pod = corev1.read_namespaced_pod("mongo", f"{namespace}-mongo")
            return pod.status.phase == "Running"
        except Exception:
            return False

    wait_until(pod_is_ready, timeout=60)


@pytest.mark.e2e_multi_cluster_mtls_test
def test_connectivity_fails_from_second_namespace(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    cluster_1_client = member_cluster_clients[0]

    service_fqdn = f"{mongodb_multi.name}-2-0-svc.{namespace}.svc.cluster.local"
    cmd = ["mongosh", "--host", service_fqdn]

    result = KubernetesTester.run_command_in_pod_container(
        "mongo",
        f"{namespace}-mongo",
        cmd,
        container="mongo",
        api_client=cluster_1_client.api_client,
    )

    failures = [
        "MongoServerSelectionError: connection <monitor> to",
        f"getaddrinfo ENOTFOUND {service_fqdn}",
        "HostNotFound",
    ]

    assert True in [
        failure in result for failure in failures
    ], f"no expected failure messages found in result: {result}"


@pytest.mark.e2e_multi_cluster_mtls_test
def test_enable_istio_injection(
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    cluster_1_client = member_cluster_clients[0]
    corev1 = kubernetes.client.CoreV1Api(api_client=cluster_1_client.api_client)
    ns: kubernetes.client.V1Namespace = corev1.read_namespace(f"{namespace}-mongo")
    ns.metadata.labels["istio-injection"] = "enabled"
    corev1.patch_namespace(f"{namespace}-mongo", ns)


@pytest.mark.e2e_multi_cluster_mtls_test
def test_delete_existing_mongo_pod(member_cluster_clients: List[MultiClusterClient], namespace: str):
    cluster_1_client = member_cluster_clients[0]
    corev1 = kubernetes.client.CoreV1Api(api_client=cluster_1_client.api_client)
    corev1.delete_namespaced_pod("mongo", f"{namespace}-mongo")

    def pod_is_deleted() -> bool:
        try:
            corev1.read_namespaced_pod("mongo", f"{namespace}-mongo")
            return False
        except kubernetes.client.ApiException:
            return True

    wait_until(pod_is_deleted, timeout=120)


@pytest.mark.e2e_multi_cluster_mtls_test
def test_create_pod_with_istio_sidecar(member_cluster_clients: List[MultiClusterClient], namespace: str):
    cluster_1_client = member_cluster_clients[0]
    corev1 = kubernetes.client.CoreV1Api(api_client=cluster_1_client.api_client)
    # create a pod with mongo installed in a separate namespace that does not have istio configured.
    corev1.create_namespaced_pod(
        f"{namespace}-mongo",
        {
            "apiVersion": "v1",
            "kind": "Pod",
            "metadata": {
                "name": "mongo",
            },
            "spec": {
                "containers": [
                    {
                        "image": "mongo",
                        "name": "mongo",
                    }
                ],
                "dnsPolicy": "ClusterFirst",
                "restartPolicy": "Never",
            },
        },
    )

    def two_containers_are_present() -> bool:
        try:
            pod: kubernetes.client.V1Pod = corev1.read_namespaced_pod("mongo", f"{namespace}-mongo")
            return len(pod.spec.containers) == 2 and pod.status.phase == "Running"
        except Exception:
            return False

    # wait for container to back up with sidecar
    wait_until(two_containers_are_present, timeout=60)


@pytest.mark.e2e_multi_cluster_mtls_test
def test_connectivity_succeeds_from_second_namespace(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    cluster_1_client = member_cluster_clients[0]
    cmd = [
        "mongosh",
        "--host",
        f"{mongodb_multi.name}-0-0-svc.{namespace}.svc.cluster.local",
    ]

    def can_connect_to_deployment() -> bool:
        result = KubernetesTester.run_command_in_pod_container(
            "mongo",
            f"{namespace}-mongo",
            cmd,
            container="mongo",
            api_client=cluster_1_client.api_client,
        )
        if "Error: network error while attempting to run command 'isMaster' on host" in result:
            return False

        if f"getaddrinfo ENOTFOUND" in result:
            return False

        if "HostNotFound" in result:
            return False

        if f"Connecting to:		mongodb://{mongodb_multi.name}-0-0-svc.{namespace}.svc.cluster.local:27017" not in result:
            return False

        return True

    wait_until(can_connect_to_deployment, timeout=60)
