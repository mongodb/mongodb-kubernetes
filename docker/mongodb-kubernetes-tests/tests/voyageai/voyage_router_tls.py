"""VoyageRouter inbound TLS, upstream TLS and certificate rotation."""

from __future__ import annotations

from pathlib import Path

import pytest
import requests
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import create_or_update_configmap, read_secret, try_load
from kubetester.certs import generate_cert, rotate_cert
from kubetester.kubetester import run_periodically
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.voyageai import VoyageAI, VoyageRouter
from pytest import fixture
from tests.common.voyageai.echo_stub import voyage_ai
from tests.common.voyageai.router_client import (
    RERANK,
    TrafficProbe,
    is_echo_from,
    is_router_error,
    model_request,
    router_pods,
    service_fqdn,
    wait_for_response,
    wait_for_router_rollout,
)

ROUTER_NAME = "secure-router"
INBOUND_TLS_SECRET = "router-tls"
UPSTREAM_CA_CONFIGMAP = "upstream-ca"
BACKEND_NAME = "secure"
BACKEND_TLS_SECRET = "secure-tls"
MODEL = "rerank-2.5"


@fixture(scope="module")
def router(namespace: str) -> VoyageRouter:
    resource = VoyageRouter(ROUTER_NAME, namespace)
    try_load(resource)
    return resource


@fixture(scope="module")
def backend(namespace: str, stub_image: str) -> VoyageAI:
    return voyage_ai(namespace, BACKEND_NAME, MODEL, stub_image=stub_image, tls_secret=BACKEND_TLS_SECRET)


@fixture(scope="module")
def ca(issuer_ca_filepath: str) -> str:
    with open(Path(issuer_ca_filepath).with_name("ca-tls.crt")) as f:
        return f.read()


def https_request(router: VoyageRouter, ca_path: str, retry_connect: bool = True):
    return model_request(router, MODEL, path=RERANK, tls=True, verify=ca_path, retry_connect=retry_connect)


@pytest.mark.e2e_voyage_router_tls
def test_install_operator(default_operator: Operator):
    default_operator.wait_for_operator_ready()


@pytest.mark.e2e_voyage_router_tls
def test_router_waits_for_inbound_certificate(router: VoyageRouter):
    router["spec"] = {"security": {"tls": {"certificateKeySecretRef": {"name": INBOUND_TLS_SECRET}}}}
    router.update()
    router.assert_reaches_phase(
        Phase.Pending, msg_regexp=f'(?i)inbound TLS certificate Secret "{INBOUND_TLS_SECRET}" not found', timeout=120
    )

    with pytest.raises(ApiException) as error:
        client.AppsV1Api().read_namespaced_deployment(router.deployment_name(), router.namespace)
    assert error.value.status == 404


@pytest.mark.e2e_voyage_router_tls
def test_router_serves_https(router: VoyageRouter, namespace: str, issuer: str, issuer_ca_filepath: str):
    generate_cert(
        namespace,
        pod=[],
        dns=[service_fqdn(router.service_name(), namespace)],
        issuer=issuer,
        secret_name=INBOUND_TLS_SECRET,
    )
    router.assert_reaches_phase(Phase.Running, timeout=600)

    wait_for_response(
        https_request(router, issuer_ca_filepath),
        lambda response: is_router_error(response, 404, "model is not selected"),
        msg="Router to answer over HTTPS",
    )


@pytest.mark.e2e_voyage_router_tls
def test_router_rejects_plaintext(router: VoyageRouter):
    with pytest.raises(requests.exceptions.ConnectionError):
        model_request(router, MODEL, path=RERANK, retry_connect=False)()


@pytest.mark.e2e_voyage_router_tls
def test_tls_backend_requires_upstream_ca(
    router: VoyageRouter, backend: VoyageAI, namespace: str, issuer: str, issuer_ca_filepath: str
):
    generate_cert(
        namespace,
        pod=[],
        dns=[service_fqdn(backend.service_name(), namespace)],
        issuer=issuer,
        secret_name=BACKEND_TLS_SECRET,
    )
    backend.update()
    backend.assert_reaches_phase(Phase.Running, timeout=600)

    router.assert_reaches_phase(
        Phase.Pending,
        msg_regexp=r"(?i)security\.upstreamTLS\.ca is required when a selected VoyageAI uses TLS",
        timeout=120,
    )
    # The Router keeps serving but withholds every backend until it can verify the TLS ones.
    ready = router.get_condition("Ready")
    reconciled = router.get_condition("Reconciled")
    assert ready is not None and ready["status"] == "True", router.get_status()
    assert reconciled is not None and reconciled["status"] == "False", router.get_status()
    wait_for_response(
        https_request(router, issuer_ca_filepath),
        lambda response: is_router_error(response, 503, "model is unavailable"),
        msg="Router to withhold the TLS backend until an upstream CA is configured",
    )


@pytest.mark.e2e_voyage_router_tls
def test_router_reports_missing_upstream_ca(router: VoyageRouter):
    router.load()
    router["spec"]["security"]["upstreamTLS"] = {"ca": {"name": UPSTREAM_CA_CONFIGMAP}}
    router.update()
    router.assert_reaches_phase(
        Phase.Pending, msg_regexp=f'(?i)upstream TLS CA ConfigMap "{UPSTREAM_CA_CONFIGMAP}" not found', timeout=120
    )


@pytest.mark.e2e_voyage_router_tls
def test_router_reports_incomplete_upstream_ca(router: VoyageRouter, namespace: str, ca: str):
    create_or_update_configmap(namespace, UPSTREAM_CA_CONFIGMAP, {"ca.pem": ca})
    router.assert_reaches_phase(
        Phase.Pending,
        msg_regexp=f'(?i)upstream TLS CA ConfigMap "{UPSTREAM_CA_CONFIGMAP}" must contain "ca.crt"',
        timeout=120,
    )


@pytest.mark.e2e_voyage_router_tls
def test_router_verifies_tls_backend(
    router: VoyageRouter, backend: VoyageAI, namespace: str, ca: str, issuer_ca_filepath: str
):
    config_hash = router.config_hash()
    old_pods = {pod.metadata.name for pod in router_pods(router)}
    create_or_update_configmap(namespace, UPSTREAM_CA_CONFIGMAP, {"ca.crt": ca})
    run_periodically(
        lambda: router.config_hash() != config_hash,
        timeout=300,
        msg="Router Deployment to pick up the upstream CA",
    )
    router.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_router_rollout(router)
    run_periodically(
        lambda: not old_pods & {pod.metadata.name for pod in router_pods(router)},
        timeout=600,
        msg="Router pods with the previous upstream CA to terminate",
    )

    response = wait_for_response(
        https_request(router, issuer_ca_filepath),
        lambda response: is_echo_from(response, BACKEND_NAME),
        msg="Router to route rerank-2.5 to the TLS backend",
    )
    assert response.headers["x-echo-authority"].split(":")[0] == service_fqdn(backend.service_name(), namespace)


@pytest.mark.e2e_voyage_router_tls
def test_upstream_ca_bundle_update_does_not_drop_requests(
    router: VoyageRouter, namespace: str, issuer_ca_filepath: str
):
    config_hash = router.config_hash()
    old_pods = {pod.metadata.name for pod in router_pods(router)}
    full_chain = Path(issuer_ca_filepath).read_text(encoding="utf-8")

    with TrafficProbe(https_request(router, issuer_ca_filepath, retry_connect=False)) as probe:
        create_or_update_configmap(namespace, UPSTREAM_CA_CONFIGMAP, {"ca.crt": full_chain})
        run_periodically(
            lambda: router.config_hash() != config_hash,
            timeout=300,
            msg="Router Deployment to pick up the updated upstream CA bundle",
        )
        wait_for_router_rollout(router)
        run_periodically(
            lambda: not old_pods & {pod.metadata.name for pod in router_pods(router)},
            timeout=600,
            msg="Router pods with the previous upstream CA bundle to terminate",
        )

    probe.assert_no_failures()
    assert is_echo_from(https_request(router, issuer_ca_filepath)(), BACKEND_NAME)


@pytest.mark.e2e_voyage_router_tls
def test_inbound_certificate_rotation_does_not_drop_requests(
    router: VoyageRouter, namespace: str, issuer_ca_filepath: str
):
    certificate = read_secret(namespace, INBOUND_TLS_SECRET)["tls.crt"]
    config_hash = router.config_hash()
    old_pods = {pod.metadata.name for pod in router_pods(router)}

    with TrafficProbe(https_request(router, issuer_ca_filepath, retry_connect=False)) as probe:
        rotate_cert(namespace, INBOUND_TLS_SECRET)
        run_periodically(
            lambda: read_secret(namespace, INBOUND_TLS_SECRET)["tls.crt"] != certificate,
            timeout=120,
            msg="cert-manager to reissue the Router certificate",
        )
        run_periodically(
            lambda: router.config_hash() != config_hash,
            timeout=300,
            msg="Router Deployment to pick up the reissued certificate",
        )
        # Keep sending until the old pods, which drain on shutdown, are gone.
        run_periodically(
            lambda: not old_pods & {pod.metadata.name for pod in router_pods(router)},
            timeout=600,
            msg="Router pods with the previous certificate to terminate",
        )

    router.assert_reaches_phase(Phase.Running, timeout=300)
    wait_for_router_rollout(router)
    probe.assert_no_failures()
    assert is_echo_from(https_request(router, issuer_ca_filepath)(), BACKEND_NAME)
