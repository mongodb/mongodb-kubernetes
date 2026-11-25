"""VoyageRouter request routing and validation.

Envoy echo stubs stand in for the VoyageAI model servers, so every routed request comes back with the
request the stub received and the name of the pod that answered.
"""

from __future__ import annotations

import base64
import gzip
import json
import os

import pytest
from kubetester import try_load
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.voyageai import VoyageAI, VoyageRouter
from pytest import fixture
from tests.common.voyageai.echo_stub import voyage_ai
from tests.common.voyageai.router_client import (
    API_PATHS,
    EMBEDDINGS,
    MULTIMODAL_EMBEDDINGS,
    RERANK,
    ROUTER_INTERNAL_HEADER,
    assert_echo_from,
    assert_router_error,
    inference_request,
    is_echo_from,
    model_request,
    router_url,
    send,
    service_fqdn,
    stub_pod,
    wait_for_response,
)

ROUTER_NAME = "routing"
EMBED_A = "embed-a"
EMBED_B = "embed-b"
EMBED_BACKENDS = (EMBED_A, EMBED_B)
RERANK_BACKEND = "rerank"
UNAVAILABLE_BACKEND = "unavailable"

EMBED_MODEL = "voyage-4"
RERANK_MODEL = "rerank-2.5"
UNAVAILABLE_MODEL = "voyage-4-lite"
UNSELECTED_MODEL = "voyage-code-3"

MODEL_REQUIRED = "request body requires a string model"
VALID_BODY = json.dumps(inference_request(EMBED_MODEL)).encode()


@fixture(scope="module")
def backends(namespace: str, stub_image: str) -> dict[str, VoyageAI]:
    return {
        EMBED_A: voyage_ai(namespace, EMBED_A, EMBED_MODEL, stub_image=stub_image),
        EMBED_B: voyage_ai(namespace, EMBED_B, EMBED_MODEL, stub_image=stub_image),
        RERANK_BACKEND: voyage_ai(namespace, RERANK_BACKEND, RERANK_MODEL, stub_image=stub_image),
        UNAVAILABLE_BACKEND: voyage_ai(
            namespace, UNAVAILABLE_BACKEND, UNAVAILABLE_MODEL, stub_image=stub_image, ready=False
        ),
    }


@fixture(scope="module")
def router(namespace: str) -> VoyageRouter:
    resource = VoyageRouter(ROUTER_NAME, namespace)
    try_load(resource)
    return resource


@fixture(scope="module")
def url(router: VoyageRouter) -> str:
    return router_url(router)


@pytest.mark.e2e_voyage_router_routing
def test_install_operator(default_operator: Operator):
    default_operator.wait_for_operator_ready()


@pytest.mark.e2e_voyage_router_routing
def test_create_backends(backends: dict[str, VoyageAI]):
    for backend in backends.values():
        backend.update()
    for name, backend in backends.items():
        backend.assert_reaches_phase(Phase.Pending if name == UNAVAILABLE_BACKEND else Phase.Running, timeout=600)


@pytest.mark.e2e_voyage_router_routing
def test_create_router(router: VoyageRouter):
    # An empty spec selects every VoyageAI in the namespace.
    router["spec"] = {}
    router.update()
    router.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_voyage_router_routing
def test_router_serves_requests(router: VoyageRouter):
    wait_for_response(
        model_request(router, RERANK_MODEL, path=RERANK),
        lambda response: is_echo_from(response, RERANK_BACKEND),
        msg="Router to route rerank-2.5 requests",
    )


@pytest.mark.e2e_voyage_router_routing
@pytest.mark.parametrize("path", API_PATHS)
def test_api_paths_are_forwarded(url: str, path: str):
    body = json.dumps(inference_request(RERANK_MODEL, query="which path?")).encode()
    assert_echo_from(send(url, path, data=body), RERANK_BACKEND, path, body)


@pytest.mark.e2e_voyage_router_routing
def test_requests_are_balanced_across_model_backends(url: str):
    pods = set()
    for _ in range(20):
        response = send(url, EMBEDDINGS, data=VALID_BODY)
        assert_echo_from(response, EMBED_BACKENDS, EMBEDDINGS, VALID_BODY)
        pods.add(stub_pod(response))

    assert any(pod.startswith(f"{EMBED_A}-") for pod in pods), pods
    assert any(pod.startswith(f"{EMBED_B}-") for pod in pods), pods


@pytest.mark.e2e_voyage_router_routing
def test_router_forwards_headers_and_rewrites_authority(url: str, router: VoyageRouter, backends: dict[str, VoyageAI]):
    body = json.dumps(inference_request(RERANK_MODEL)).encode()
    response = send(url, RERANK, data=body, headers={"Authorization": "Bearer e2e", "x-e2e-trace": "voyage"})

    assert_echo_from(response, RERANK_BACKEND, RERANK, body)
    assert response.headers["x-echo-method"] == "POST"
    assert response.headers["x-echo-authorization"] == "Bearer e2e"
    assert response.headers["x-echo-x-e2e-trace"] == "voyage"
    rerank_service = service_fqdn(backends[RERANK_BACKEND].service_name(), router.namespace)
    assert response.headers["x-echo-authority"].split(":")[0] == rerank_service
    router_service = service_fqdn(router.service_name(), router.namespace)
    assert response.headers["x-echo-x-forwarded-host"].split(":")[0] == router_service


@pytest.mark.e2e_voyage_router_routing
def test_clients_cannot_choose_the_upstream_cluster(url: str):
    spoofed = {ROUTER_INTERNAL_HEADER: f"voyage_router_{RERANK_MODEL}"}

    response = send(url, EMBEDDINGS, data=VALID_BODY, headers=spoofed)
    assert_echo_from(response, EMBED_BACKENDS, EMBEDDINGS, VALID_BODY)
    assert f"x-echo-{ROUTER_INTERNAL_HEADER}" not in response.headers

    response = send(url, RERANK, json_body=inference_request(UNSELECTED_MODEL), headers=spoofed)
    assert_router_error(response, 404, "model is not selected")


@pytest.mark.e2e_voyage_router_routing
@pytest.mark.parametrize("content_type", ["application/json; charset=utf-8", "Application/JSON"])
def test_json_content_type_variants_are_accepted(url: str, content_type: str):
    response = send(url, EMBEDDINGS, data=VALID_BODY, content_type=content_type)
    assert_echo_from(response, EMBED_BACKENDS, EMBEDDINGS, VALID_BODY)


@pytest.mark.e2e_voyage_router_routing
def test_large_multimodal_request_is_forwarded_intact(url: str):
    image = base64.b64encode(os.urandom(6 * 1024 * 1024)).decode()
    body = json.dumps(
        {
            "model": EMBED_MODEL,
            "inputs": [{"content": [{"type": "image_base64", "image_base64": f"data:image/png;base64,{image}"}]}],
        }
    ).encode()
    assert len(body) > 8 * 1024 * 1024

    assert_echo_from(send(url, MULTIMODAL_EMBEDDINGS, data=body), EMBED_BACKENDS, MULTIMODAL_EMBEDDINGS, body)


@pytest.mark.e2e_voyage_router_routing
@pytest.mark.parametrize(
    "path,request_args,status,message",
    [
        pytest.param("/v1/unknown", {"data": VALID_BODY}, 404, "unknown path", id="unknown-path"),
        pytest.param(EMBEDDINGS, {"method": "GET"}, 405, "method not allowed", id="get"),
        pytest.param(
            EMBEDDINGS,
            {"data": VALID_BODY, "content_type": "text/plain"},
            415,
            "content type must be application/json",
            id="text-content-type",
        ),
        pytest.param(
            EMBEDDINGS,
            {"data": VALID_BODY, "content_type": None},
            415,
            "content type must be application/json",
            id="missing-content-type",
        ),
        pytest.param(
            EMBEDDINGS,
            {"data": gzip.compress(VALID_BODY), "headers": {"Content-Encoding": "gzip"}},
            415,
            "compressed request bodies are not supported",
            id="gzip",
        ),
        pytest.param(EMBEDDINGS, {"data": b"not json"}, 400, MODEL_REQUIRED, id="not-json"),
        pytest.param(EMBEDDINGS, {"data": b""}, 400, MODEL_REQUIRED, id="empty-body"),
        pytest.param(EMBEDDINGS, {"json_body": {"input": ["hello"]}}, 400, MODEL_REQUIRED, id="missing-model"),
        pytest.param(
            EMBEDDINGS, {"json_body": {"model": 4, "input": ["hello"]}}, 400, MODEL_REQUIRED, id="numeric-model"
        ),
        pytest.param(EMBEDDINGS, {"json_body": inference_request("")}, 400, MODEL_REQUIRED, id="empty-model"),
        pytest.param(
            EMBEDDINGS,
            {"json_body": inference_request(UNSELECTED_MODEL)},
            404,
            "model is not selected",
            id="model-without-voyageai",
        ),
        pytest.param(
            EMBEDDINGS,
            {"json_body": inference_request("voyage-unknown")},
            404,
            "model is not selected",
            id="unknown-model",
        ),
        pytest.param(
            EMBEDDINGS,
            {"json_body": inference_request(UNAVAILABLE_MODEL)},
            503,
            "model is unavailable",
            id="model-without-ready-voyageai",
        ),
    ],
)
@pytest.mark.e2e_voyage_router_routing
def test_invalid_requests_are_rejected(url: str, path: str, request_args: dict, status: int, message: str):
    assert_router_error(send(url, path, **request_args), status, message)


@pytest.mark.e2e_voyage_router_routing
def test_request_body_limit_is_enforced(url: str, router: VoyageRouter):
    router.load()
    router["spec"]["requestBodyLimitBytes"] = 2048
    router.update()
    router.assert_reaches_phase(Phase.Running, timeout=300)

    oversized = json.dumps(inference_request(EMBED_MODEL, input=["x" * 4096])).encode()
    response = wait_for_response(
        lambda: send(url, EMBEDDINGS, data=oversized),
        lambda response: response.status_code == 413,
        msg="Router to apply the 2 KiB request body limit",
    )
    error = response.json()["error"]
    assert error["type"] == "invalid_request_error"
    assert "too large" in error["message"].lower()

    assert_echo_from(send(url, EMBEDDINGS, data=VALID_BODY), EMBED_BACKENDS, EMBEDDINGS, VALID_BODY)
