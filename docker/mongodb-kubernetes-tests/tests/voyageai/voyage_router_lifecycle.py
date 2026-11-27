"""VoyageRouter owned resources, status, Deployment overrides, drift repair, scaling and deletion."""

from __future__ import annotations

from typing import Callable

import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.kubetester import run_periodically
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.voyageai import VoyageAI, VoyageRouter
from pytest import fixture
from tests.common.voyageai.echo_stub import voyage_ai
from tests.common.voyageai.router_client import (
    EMBEDDINGS,
    ROUTER_PORT,
    inference_request,
    is_echo_from,
    model_request,
    ready_router_pods,
    router_pods,
    send,
    wait_for_response,
)

ROUTER_NAME = "lifecycle"
BACKEND_NAME = "embed"
MODEL = "voyage-4"
CONFIG_KEYS = {"bootstrap.json", "cds.json", "lds.json"}

RESOURCE_REQUIREMENTS = {
    "requests": {"cpu": "100m", "memory": "128Mi"},
    "limits": {"cpu": "500m", "memory": "256Mi"},
}
DEPLOYMENT_LABELS = {"e2e-deployment-label": ROUTER_NAME}
DEPLOYMENT_ANNOTATIONS = {"e2e-deployment-annotation": ROUTER_NAME}
POD_LABELS = {"e2e-pod-label": ROUTER_NAME}
POD_ANNOTATIONS = {"e2e-pod-annotation": ROUTER_NAME}


@fixture(scope="module")
def router(namespace: str) -> VoyageRouter:
    resource = VoyageRouter(ROUTER_NAME, namespace)
    try_load(resource)
    return resource


@fixture(scope="module")
def backend(namespace: str, stub_image: str) -> VoyageAI:
    return voyage_ai(namespace, BACKEND_NAME, MODEL, stub_image=stub_image)


def is_deleted(read: Callable[[], object]) -> bool:
    try:
        read()
    except ApiException as e:
        if e.status == 404:
            return True
        raise
    return False


def assert_owned_by(obj, router: VoyageRouter):
    assert obj.metadata.labels.items() >= router.managed_labels().items(), obj.metadata.labels
    owners = [owner for owner in obj.metadata.owner_references or [] if owner.controller]
    assert len(owners) == 1, obj.metadata.owner_references
    assert owners[0].kind == "VoyageRouter"
    assert owners[0].name == router.name
    assert owners[0].uid == router["metadata"]["uid"]


@pytest.mark.e2e_voyage_router_lifecycle
def test_install_operator(default_operator: Operator):
    default_operator.wait_for_operator_ready()


@pytest.mark.e2e_voyage_router_lifecycle
def test_create_backend(backend: VoyageAI):
    backend.update()
    backend.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_voyage_router_lifecycle
def test_create_router(router: VoyageRouter):
    router["spec"] = {
        "replicas": 2,
        "resourceRequirements": RESOURCE_REQUIREMENTS,
        "deployment": {
            "metadata": {"labels": DEPLOYMENT_LABELS, "annotations": DEPLOYMENT_ANNOTATIONS},
            "spec": {"template": {"metadata": {"labels": POD_LABELS, "annotations": POD_ANNOTATIONS}}},
        },
    }
    router.update()
    router.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_voyage_router_lifecycle
def test_router_conditions(router: VoyageRouter):
    router.load()
    for condition_type in ("Ready", "Reconciled"):
        condition = router.get_condition(condition_type)
        assert condition is not None, router.get_status()
        assert condition["status"] == "True"
        assert condition["reason"] == "Running"
        assert condition["message"] == "Router resources are current"
        assert condition["observedGeneration"] == router.get_generation()


@pytest.mark.e2e_voyage_router_lifecycle
def test_router_deployment(router: VoyageRouter):
    deployment = router.read_deployment()
    assert_owned_by(deployment, router)
    assert deployment.metadata.labels.items() >= DEPLOYMENT_LABELS.items()
    assert deployment.metadata.annotations.items() >= DEPLOYMENT_ANNOTATIONS.items()
    assert deployment.spec.replicas == 2
    assert deployment.spec.selector.match_labels == router.pod_labels()

    template = deployment.spec.template
    assert template.metadata.labels.items() >= {**router.pod_labels(), **POD_LABELS}.items()
    assert template.metadata.annotations.items() >= POD_ANNOTATIONS.items()
    assert template.metadata.annotations.get(router.CONFIG_HASH_ANNOTATION)

    envoy = next(c for c in template.spec.containers if c.name == "envoy")
    assert envoy.resources.requests == RESOURCE_REQUIREMENTS["requests"]
    assert envoy.resources.limits == RESOURCE_REQUIREMENTS["limits"]


@pytest.mark.e2e_voyage_router_lifecycle
def test_router_service(router: VoyageRouter):
    service = router.read_service()
    assert_owned_by(service, router)
    assert service.spec.type == "ClusterIP"
    assert service.spec.selector == router.pod_labels()
    assert [(port.name, port.port, port.target_port) for port in service.spec.ports] == [
        ("http", ROUTER_PORT, ROUTER_PORT)
    ]


@pytest.mark.e2e_voyage_router_lifecycle
def test_router_configmap(router: VoyageRouter):
    configmap = router.read_configmap()
    assert_owned_by(configmap, router)
    assert set(configmap.data) == CONFIG_KEYS


@pytest.mark.e2e_voyage_router_lifecycle
def test_every_router_pod_routes(router: VoyageRouter):
    pods = ready_router_pods(router)
    assert len(pods) == 2
    for pod in pods:
        assert pod.metadata.labels.items() >= POD_LABELS.items()
        assert pod.metadata.annotations.items() >= POD_ANNOTATIONS.items()
        wait_for_response(
            lambda: send(f"http://{pod.status.pod_ip}:{ROUTER_PORT}", EMBEDDINGS, json_body=inference_request(MODEL)),
            lambda response: is_echo_from(response, BACKEND_NAME),
            msg=f"Router pod {pod.metadata.name} to route voyage-4",
        )


@pytest.mark.e2e_voyage_router_lifecycle
def test_configmap_drift_is_repaired(router: VoyageRouter):
    # Tampering with cds.json or lds.json would reach Envoy before the repair, so add an unrelated key instead.
    client.CoreV1Api().patch_namespaced_config_map(
        router.configmap_name(), router.namespace, {"data": {"tampered.json": "{}"}}
    )
    run_periodically(
        lambda: set(router.read_configmap().data) == CONFIG_KEYS,
        timeout=120,
        msg="Router ConfigMap to be restored",
    )


@pytest.mark.e2e_voyage_router_lifecycle
def test_deleted_service_is_recreated(router: VoyageRouter):
    old_uid = router.read_service().metadata.uid
    client.CoreV1Api().delete_namespaced_service(router.service_name(), router.namespace)

    def recreated() -> bool:
        try:
            return router.read_service().metadata.uid != old_uid
        except ApiException as e:
            if e.status == 404:
                return False
            raise

    run_periodically(recreated, timeout=120, msg="Router Service to be recreated")
    wait_for_response(
        model_request(router, MODEL),
        lambda response: is_echo_from(response, BACKEND_NAME),
        msg="recreated Router Service to route voyage-4",
    )


@pytest.mark.e2e_voyage_router_lifecycle
def test_deployment_drift_is_repaired(router: VoyageRouter):
    client.AppsV1Api().patch_namespaced_deployment(
        router.deployment_name(), router.namespace, {"spec": {"replicas": 3}}
    )
    run_periodically(
        lambda: router.read_deployment().spec.replicas == 2,
        timeout=120,
        msg="Router Deployment replicas to be restored",
    )


@pytest.mark.e2e_voyage_router_lifecycle
def test_router_scales_down(router: VoyageRouter):
    router.load()
    router["spec"]["replicas"] = 1
    router.update()
    router.assert_reaches_phase(Phase.Running, timeout=300)
    # Removed pods drain for 30s before they terminate.
    run_periodically(lambda: len(router_pods(router)) == 1, timeout=300, msg="Router to scale down to 1 pod")
    assert len(ready_router_pods(router)) == 1
    wait_for_response(
        model_request(router, MODEL),
        lambda response: is_echo_from(response, BACKEND_NAME),
        msg="scaled-down Router to route voyage-4",
    )


@pytest.mark.e2e_voyage_router_lifecycle
def test_router_deletion_removes_owned_resources(router: VoyageRouter, backend: VoyageAI):
    router.delete()
    run_periodically(
        lambda: all(
            is_deleted(read)
            for read in (
                router.read_deployment,
                router.read_service,
                router.read_configmap,
            )
        ),
        timeout=300,
        msg="Router Deployment, Service and ConfigMap to be garbage collected",
    )
    backend.assert_reaches_phase(Phase.Running, timeout=60)
