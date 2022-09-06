from typing import Dict, List
from pytest import mark, fixture

import kubernetes
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture
from kubernetes import client
from kubeobject import CustomObject
import time

from kubetester import delete_pod, get_pod_when_ready


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace
    )
    resource["spec"]["persistent"] = False
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource


# more details https://istio.io/latest/docs/tasks/traffic-management/egress/egress-control/
@fixture(scope="module")
def service_entry(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    service_entry = CustomObject(
        name="cluster-block",
        namespace=namespace,
        kind="ServiceEntry",
        plural="serviceentries",
        group="networking.istio.io",
        version="v1beta1",
        api_client=central_cluster_client,
    )

    service_entry["spec"] = {
        # by default the access mode is set to "REGISTRY_ONLY" which means only the hosts specified
        # here would be accessible from the operator pod
        "hosts": [
            "cloud-qa.mongodb.com",
            "api.e2e.cluster1.mongokubernetes.com",
            "api.e2e.cluster2.mongokubernetes.com",
            "api.e2e.cluster3.mongokubernetes.com",
        ],
        "location": "MESH_EXTERNAL",
        "ports": [{"name": "https", "number": 443, "protocol": "HTTPS"}],
        "resolution": "DNS",
    }
    service_entry.api = kubernetes.client.CustomObjectsApi(
        api_client=central_cluster_client
    )
    return service_entry


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_label_namespace(
    namespace: str, central_cluster_client: kubernetes.client.ApiClient
):

    api = client.CoreV1Api(api_client=central_cluster_client)

    labels = {"istio-injection": "enabled"}
    ns = api.read_namespace(name=namespace)

    ns.metadata.labels.update(labels)
    api.replace_namespace(name=namespace, body=ns)


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_create_service_entry(service_entry: CustomObject):
    service_entry.create()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.create()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_update_service_entry_block_cluster3_traffic(service_entry: CustomObject):
    service_entry.load()
    service_entry["spec"]["hosts"] = [
        "cloud-qa.mongodb.com",
        "api.e2e.cluster1.mongokubernetes.com",
        "api.e2e.cluster2.mongokubernetes.com",
    ]
    service_entry.update()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_update_mongodb_multi_to_failed_state(
    mongodb_multi: MongoDBMulti,
    multi_cluster_operator: Operator,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):

    # it takes couple of secs here for the Istio configuration to take effect, i.e the operator
    # not being able to talk to cluster3, so we patch the CR a couple of times.
    n = 0
    while n < 10:
        mongodb_multi.load()
        phase = mongodb_multi.get_status_phase()

        if phase == Phase.Pending or phase == Phase.Reconciling:
            continue

        elif phase == Phase.Running:
            mongodb_multi["metadata"]["labels"] = {"foo": str(n)}
            try:
                mongodb_multi.update()
            except client.rest.ApiException as e:
                if e.status == 409:
                    continue
            n += 1

        elif phase == Phase.Failed:
            break
        time.sleep(4)

    mongodb_multi.assert_reaches_phase(
        Phase.Failed,
        msg_regexp="Failed to create service: multi-replica-set-svc in cluster: e2e.cluster3.mongokubernetes.com",
        timeout=500,
    )


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_operator_pod_restart(multi_cluster_operator: Operator):
    multi_cluster_operator.restart_operator_deployment()
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_mongodb_multi_is_in_failed_state(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Failed)


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_replica_set_is_reachable_after_operator_restart(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_unblock_traffic_cluster3(service_entry: CustomObject):
    service_entry.load()
    service_entry["spec"]["hosts"] = [
        "cloud-qa.mongodb.com",
        "api.e2e.cluster1.mongokubernetes.com",
        "api.e2e.cluster2.mongokubernetes.com",
        "api.e2e.cluster3.mongokubernetes.com",
    ]
    service_entry.update()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_mdbm_reaches_running_state(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_block_traffic_cluster3(service_entry: CustomObject):
    service_entry.load()
    service_entry["spec"]["hosts"] = [
        "cloud-qa.mongodb.com",
        "api.e2e.cluster1.mongokubernetes.com",
        "api.e2e.cluster2.mongokubernetes.com",
    ]
    service_entry.update()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_failover_annotation_present(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    assert (
        mongodb_multi["metadata"]["annotations"]["failedCluster"]
        == "e2e.cluster3.mongokubernetes.com"
    )
