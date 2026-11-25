"""HTTP helpers for VoyageRouter e2e tests. Tests run inside the cluster and reach the Router Service by DNS.

Every request opens a new connection. The Router hot-reloads listeners and existing connections stay on
the listener they were accepted by, so a pooled connection could keep observing the previous configuration.
"""

from __future__ import annotations

import json
import threading
from typing import Callable, Optional, Sequence, Union

import requests
from kubernetes import client
from kubetester import list_matching_pods, pod_is_ready
from kubetester.kubetester import run_periodically
from kubetester.voyageai import VoyageRouter
from requests.adapters import HTTPAdapter
from tests import test_logger
from urllib3.util.retry import Retry

logger = test_logger.get_test_logger(__name__)

ROUTER_PORT = 8080
ENVOY_ADMIN_PORT = 9901
CLUSTER_DOMAIN = "cluster.local"
REQUEST_TIMEOUT_SECONDS = 30
# The kubelet refreshes ConfigMap volumes, which carry the Router's xDS files, on its sync period of up to a minute or two.
CONFIG_PROPAGATION_TIMEOUT_SECONDS = 300

EMBEDDINGS = "/v1/embeddings"
CONTEXTUALIZED_EMBEDDINGS = "/v1/contextualizedembeddings"
MULTIMODAL_EMBEDDINGS = "/v1/multimodalembeddings"
RERANK = "/v1/rerank"
API_PATHS = (EMBEDDINGS, CONTEXTUALIZED_EMBEDDINGS, MULTIMODAL_EMBEDDINGS, RERANK)

ROUTER_INTERNAL_HEADER = "x-mongodb-voyage-router-cluster"

Verify = Union[bool, str]
Backends = Union[str, Sequence[str]]


def service_fqdn(service: str, namespace: str) -> str:
    return f"{service}.{namespace}.svc.{CLUSTER_DOMAIN}"


def router_url(router: VoyageRouter, tls: bool = False) -> str:
    scheme = "https" if tls else "http"
    return f"{scheme}://{service_fqdn(router.service_name(), router.namespace)}:{ROUTER_PORT}"


def model_request(
    router: VoyageRouter,
    model: str,
    path: str = EMBEDDINGS,
    *,
    tls: bool = False,
    verify: Verify = True,
    retry_connect: bool = True,
) -> Callable[[], requests.Response]:
    return lambda: send(
        router_url(router, tls=tls),
        path,
        json_body=inference_request(model),
        verify=verify,
        retry_connect=retry_connect,
    )


def inference_request(model: str, **fields) -> dict:
    return {"model": model, "input": ["hello voyage"], **fields}


def send(
    url: str,
    path: str,
    *,
    json_body: Optional[dict] = None,
    data: Union[bytes, str, None] = None,
    content_type: Optional[str] = "application/json",
    headers: Optional[dict[str, str]] = None,
    method: str = "POST",
    verify: Verify = True,
    retry_connect: bool = True,
) -> requests.Response:
    """Sends one request on a new connection. Only failed connection attempts are retried: responses,
    including 5xx, are what tests assert."""
    request_headers = dict(headers or {})
    if content_type is not None:
        request_headers["Content-Type"] = content_type
    if json_body is not None:
        data = json.dumps(json_body).encode()
    retries = Retry(
        total=6 if retry_connect else 0,
        connect=6 if retry_connect else 0,
        read=False,
        status=0,
        other=0,
        redirect=0,
        backoff_factor=1,
        raise_on_status=False,
    )
    with requests.Session() as session:
        session.mount("http://", HTTPAdapter(max_retries=retries))
        session.mount("https://", HTTPAdapter(max_retries=retries))
        return session.request(
            method, url + path, data=data, headers=request_headers, timeout=REQUEST_TIMEOUT_SECONDS, verify=verify
        )


def is_router_error(response: requests.Response, status: int, message: str) -> bool:
    if response.status_code != status:
        return False
    try:
        return response.json() == {"error": {"message": message, "type": "invalid_request_error"}}
    except ValueError:
        return False


def assert_router_error(response: requests.Response, status: int, message: str):
    assert is_router_error(
        response, status, message
    ), f"expected {status} {message!r}, got {response.status_code}: {response.text[:500]}"


def is_echo_from(response: requests.Response, backends: Backends) -> bool:
    """True when the echo stub of one of ``backends`` answered. Stub pods are named after their Deployment, which is
    named after the VoyageAI."""
    names = (backends,) if isinstance(backends, str) else tuple(backends)
    pod = response.headers.get("x-stub-pod", "")
    return response.status_code == 200 and pod.startswith(tuple(f"{name}-" for name in names))


def assert_echo_from(response: requests.Response, backends: Backends, path: str, body: Optional[bytes] = None):
    assert is_echo_from(
        response, backends
    ), f"expected an echo from {backends}, got {response.status_code}: {response.headers} {response.text[:500]}"
    assert response.headers.get("x-echo-path") == path, response.headers
    if body is not None:
        assert response.content == body


def stub_pod(response: requests.Response) -> str:
    return response.headers["x-stub-pod"]


def wait_for_response(
    request: Callable[[], requests.Response],
    accept: Callable[[requests.Response], bool],
    msg: str,
    timeout: int = CONFIG_PROPAGATION_TIMEOUT_SECONDS,
) -> requests.Response:
    last: dict[str, requests.Response] = {}

    def check():
        try:
            response = request()
        except requests.RequestException as e:
            return False, f"request failed: {e}"
        last["response"] = response
        return accept(response), f"got {response.status_code}: {response.text[:300]}"

    run_periodically(check, timeout=timeout, sleep_time=3, msg=msg)
    return last["response"]


def router_pods(router: VoyageRouter) -> list[client.V1Pod]:
    return list_matching_pods(router.namespace, label_selector=router.pod_label_selector())


def ready_router_pods(router: VoyageRouter) -> list[client.V1Pod]:
    return [pod for pod in router_pods(router) if pod.metadata.deletion_timestamp is None and pod_is_ready(pod)]


def envoy_stats(pod: client.V1Pod, prefix: str) -> dict[str, int]:
    response = requests.get(
        f"http://{pod.status.pod_ip}:{ENVOY_ADMIN_PORT}/stats",
        params={"filter": f"^{prefix}"},
        timeout=REQUEST_TIMEOUT_SECONDS,
    )
    response.raise_for_status()
    stats = {}
    for line in response.text.splitlines():
        name, separator, value = line.partition(": ")
        if separator and value.strip().isdigit():
            stats[name] = int(value)
    return stats


def xds_update_counts(pod: client.V1Pod) -> tuple[int, int]:
    """Returns how many CDS and LDS updates the pod's Envoy accepted."""
    cds = envoy_stats(pod, "cluster_manager.cds.update_success")
    lds = envoy_stats(pod, "listener_manager.lds.update_success")
    return cds["cluster_manager.cds.update_success"], lds["listener_manager.lds.update_success"]


class TrafficProbe:
    """Sends a request every ``interval`` seconds from a background thread, each on a new connection and without
    retries, and records every response other than ``expected_status``."""

    def __init__(self, request: Callable[[], requests.Response], expected_status: int = 200, interval: float = 0.2):
        self._request = request
        self._expected_status = expected_status
        self._interval = interval
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, name="voyage-traffic-probe", daemon=True)
        self.attempts = 0
        self.failures: list[str] = []

    def __enter__(self) -> "TrafficProbe":
        self._thread.start()
        return self

    def __exit__(self, *exc_info):
        self._stop.set()
        self._thread.join()

    def _run(self):
        while not self._stop.is_set():
            self.attempts += 1
            try:
                response = self._request()
                if response.status_code != self._expected_status:
                    self.failures.append(f"{response.status_code}: {response.text[:200]}")
            except requests.RequestException as e:
                self.failures.append(f"{type(e).__name__}: {e}")
            self._stop.wait(self._interval)

    def assert_no_failures(self, min_attempts: int = 20):
        logger.info(f"Traffic probe sent {self.attempts} requests, {len(self.failures)} failed")
        assert self.attempts >= min_attempts, f"traffic probe sent only {self.attempts} requests"
        assert not self.failures, f"{len(self.failures)}/{self.attempts} requests failed: {self.failures[:10]}"
