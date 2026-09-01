"""Process-global registry that toggles CR fan-out on MongoDBMulti. It exists because in
decentralized operator mode each cluster's member-spec fence only ever looks at its OWN copy of
the CR, while legacy e2e tests write the CR once on a single (central) cluster; the
DECENTRALIZED_E2E operator fixture enables this registry so those writes replicate everywhere.
"""

from __future__ import annotations

from typing import Dict, Optional

from kubernetes import client

_clients: Dict[str, client.ApiClient] = {}
_primary: Optional[str] = None


def enable(clients: Dict[str, client.ApiClient], primary: str) -> None:
    if primary not in clients:
        raise ValueError(f"primary cluster {primary!r} not among clients {sorted(clients)}")

    global _clients, _primary
    _clients = dict(clients)
    _primary = primary


def disable() -> None:
    global _clients, _primary
    _clients = {}
    _primary = None


def is_enabled() -> bool:
    return _primary is not None


def primary_name() -> Optional[str]:
    return _primary


def peer_clients() -> Dict[str, client.ApiClient]:
    return {name: c for name, c in _clients.items() if name != _primary}


def all_clients() -> Dict[str, client.ApiClient]:
    return dict(_clients)


# --- Secret/ConfigMap fan-out: the pre-provisioning contract for issuance-time materials ---
#
# Decentralized operators read TLS/CA material only from their own cluster (the legacy
# central→member copy has no counterpart), and the AC carries a single cert path that is correct
# only if every cluster's copy is byte-identical. So the copy happens here, at setup time, and
# identity is ASSERTED, not assumed — a skewed copy must fail the run, never ship.


def fan_out_secret(namespace: str, name: str, source_api_client: Optional[client.ApiClient] = None) -> None:
    """Copies a Secret from the source cluster (primary by default) to every peer in the registry,
    preserving its type, then asserts all copies are byte-identical. No-op when disabled."""
    if not is_enabled():
        return

    source = client.CoreV1Api(api_client=_source_client(source_api_client)).read_namespaced_secret(name, namespace)
    body = client.V1Secret(
        metadata=client.V1ObjectMeta(name=name, namespace=namespace),
        data=source.data,
        type=source.type,
    )
    source_content = {"data": source.data, "type": source.type}

    errors: Dict[str, Exception] = {}
    copies: Dict[str, dict] = {}
    for cluster_name, api_client in peer_clients().items():
        corev1 = client.CoreV1Api(api_client=api_client)
        try:
            _create_or_replace(
                lambda: corev1.create_namespaced_secret(namespace, body),
                lambda: corev1.replace_namespaced_secret(name, namespace, body),
            )
            peer = corev1.read_namespaced_secret(name, namespace)
            copies[cluster_name] = {"data": peer.data, "type": peer.type}
        except Exception as e:
            errors[cluster_name] = e

    if errors:
        raise _distribution_error("Secret", namespace, name, errors)
    assert_copies_identical("Secret", namespace, name, source_content, copies)


def fan_out_config_map(namespace: str, name: str, source_api_client: Optional[client.ApiClient] = None) -> None:
    """Copies a ConfigMap from the source cluster (primary by default) to every peer in the
    registry, then asserts all copies are byte-identical. No-op when disabled."""
    if not is_enabled():
        return

    source = client.CoreV1Api(api_client=_source_client(source_api_client)).read_namespaced_config_map(name, namespace)
    body = client.V1ConfigMap(
        metadata=client.V1ObjectMeta(name=name, namespace=namespace),
        data=source.data,
        binary_data=source.binary_data,
    )
    source_content = {"data": source.data, "binaryData": source.binary_data}

    errors: Dict[str, Exception] = {}
    copies: Dict[str, dict] = {}
    for cluster_name, api_client in peer_clients().items():
        corev1 = client.CoreV1Api(api_client=api_client)
        try:
            _create_or_replace(
                lambda: corev1.create_namespaced_config_map(namespace, body),
                lambda: corev1.replace_namespaced_config_map(name, namespace, body),
            )
            peer = corev1.read_namespaced_config_map(name, namespace)
            copies[cluster_name] = {"data": peer.data, "binaryData": peer.binary_data}
        except Exception as e:
            errors[cluster_name] = e

    if errors:
        raise _distribution_error("ConfigMap", namespace, name, errors)
    assert_copies_identical("ConfigMap", namespace, name, source_content, copies)


def assert_copies_identical(kind: str, namespace: str, name: str, source: dict, copies: Dict[str, dict]) -> None:
    mismatched = sorted(cluster for cluster, content in copies.items() if content != source)
    if mismatched:
        raise AssertionError(
            f"{kind} {namespace}/{name} is not byte-identical on cluster(s) {', '.join(mismatched)}: "
            "the pre-provisioning contract requires identical copies everywhere"
        )


def _source_client(explicit: Optional[client.ApiClient]) -> client.ApiClient:
    return explicit if explicit is not None else _clients[_primary]


def _create_or_replace(create, replace) -> None:
    try:
        create()
    except client.ApiException as e:
        if e.status != 409:
            raise
        replace()


def _distribution_error(kind: str, namespace: str, name: str, errors: Dict[str, Exception]) -> Exception:
    details = "; ".join(f"{cluster}: {error}" for cluster, error in sorted(errors.items()))
    return Exception(f"decentralized fan-out of {kind} {namespace}/{name} failed on cluster(s): {details}")
