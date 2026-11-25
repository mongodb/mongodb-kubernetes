"""Envoy echo stub that stands in for a VoyageAI model server.

Evergreen has no GPU hosts, so e2e tests override the VoyageAI Deployment to
run Envoy with a Lua filter instead of the model image. The stub answers the
VoyageAI health probes and echoes every other request back as a 200: the body
and content type unchanged, each request header as ``x-echo-<name>``, the
method, path and authority as ``x-echo-method``, ``x-echo-path`` and
``x-echo-authority``, and its own pod name as ``x-stub-pod``.
"""

from __future__ import annotations

from pathlib import Path
from typing import Optional

import yaml
from kubetester.voyageai import VoyageAI

DEFAULT_ECHO_STUB_IMAGE = "envoyproxy/envoy:v1.37.11"
VOYAGEAI_CONTAINER_NAME = "voyageai"
STUB_VERSION = "e2e-echo-stub"
STUB_PORT = 8080
# Nothing listens on this port, so a readiness probe pointed at it keeps the stub pod unready.
CLOSED_PORT = 1
FIXTURES_DIR = Path(__file__).parent / "fixtures"


def echo_stub_image(operator_installation_config: dict[str, str]) -> str:
    """Reuses the Envoy image the operator is configured with, so the stub needs no extra image pull."""
    return operator_installation_config.get("search.envoyImage") or DEFAULT_ECHO_STUB_IMAGE


def echo_stub_envoy_config(port: int = STUB_PORT, tls: bool = False) -> str:
    config = yaml.safe_load((FIXTURES_DIR / "echo-envoy.yaml").read_text(encoding="utf-8"))
    listener = config["static_resources"]["listeners"][0]
    listener["address"]["socket_address"]["port_value"] = port
    filter_chain = listener["filter_chains"][0]
    lua_filter = filter_chain["filters"][0]["typed_config"]["http_filters"][0]["typed_config"]
    lua_filter["default_source_code"]["inline_string"] = (FIXTURES_DIR / "echo.lua").read_text(encoding="utf-8")
    if not tls:
        filter_chain.pop("transport_socket")
    return yaml.safe_dump(config, sort_keys=False)


def echo_stub_container(image: str, port: int = STUB_PORT, tls: bool = False, ready: bool = True) -> dict:
    container = {
        "name": VOYAGEAI_CONTAINER_NAME,
        "image": image,
        "command": ["/usr/local/bin/envoy"],
        "args": [
            "--log-level",
            "warn",
            "--disable-hot-restart",
            "--config-yaml",
            echo_stub_envoy_config(port, tls),
        ],
        # Non-nil requests and limits replace the operator defaults, which drops nvidia.com/gpu.
        "resources": {
            "requests": {"cpu": "50m", "memory": "64Mi"},
            "limits": {"cpu": "500m", "memory": "256Mi"},
        },
    }
    if not ready:
        # The override replaces the operator's whole httpGet action, so only the port needs to break.
        container["readinessProbe"] = {"httpGet": {"path": "/health/readiness", "port": CLOSED_PORT}}
    return container


def echo_stub_deployment_override(container: dict) -> dict:
    return {"spec": {"template": {"spec": {"containers": [container]}}}}


def set_echo_stub(resource: VoyageAI, stub_image: str, ready: bool = True):
    """Replaces the model server in ``resource``'s Deployment with the echo stub. With ``ready=False`` the stub
    pod never becomes ready, which keeps the VoyageAI Pending. Call ``update()`` to apply a change."""
    tls = bool(((resource["spec"].get("security") or {}).get("tls") or {}).get("certificateKeySecretRef"))
    resource["spec"]["deployment"] = echo_stub_deployment_override(
        echo_stub_container(stub_image, tls=tls, ready=ready)
    )


def voyage_ai(
    namespace: str,
    name: str,
    model: str,
    *,
    stub_image: str,
    labels: Optional[dict[str, str]] = None,
    tls_secret: Optional[str] = None,
    ready: bool = True,
) -> VoyageAI:
    """Builds, but does not create, a VoyageAI resource whose model server is replaced by the echo stub."""
    resource = VoyageAI(name, namespace)
    if labels:
        resource["metadata"]["labels"] = dict(labels)
    resource["spec"] = {"model": model, "version": STUB_VERSION}
    if tls_secret is not None:
        resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": tls_secret}}}
    set_echo_stub(resource, stub_image, ready=ready)
    return resource
