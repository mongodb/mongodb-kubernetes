"""Single source of truth for kind docker network IPs used by E2E tests.

Mirrors `scripts/funcs/kind_network` (bash). Tests that hardcoded
``172.18.255.x`` should import from here so the kind subnet stays
configurable end-to-end via ``KIND_NETWORK_SUBNET`` (default
``172.18.0.0/16``).

Convention (10-IP-wide slots within ``${PREFIX}.255.0/24``):
    .200-.209  kind-e2e-operator (and the single 'kind' cluster)
    .210-.219  kind-e2e-cluster-1
    .220-.229  kind-e2e-cluster-2
    .230-.239  kind-e2e-cluster-3
"""

from __future__ import annotations

import os
from ipaddress import IPv4Address, IPv4Network


def _prefix(subnet: str) -> str:
    """Return the leading two octets of an IPv4 ``a.b.c.d/p`` subnet string."""
    network = IPv4Network(subnet)
    octets = str(network.network_address).split(".")
    return f"{octets[0]}.{octets[1]}"


# Defaults preserve the historical 172.18.0.0/16 hardcoding so existing
# tests/tooling keep working unchanged. Override KIND_NETWORK_SUBNET in
# the environment to use a different /16 (the bash side reads the same
# variable from scripts/funcs/kind_network).
KIND_NETWORK_SUBNET: str = os.environ.get("KIND_NETWORK_SUBNET", "172.18.0.0/16")
KIND_NETWORK_PREFIX: str = _prefix(KIND_NETWORK_SUBNET)


def kind_lb_ip(cluster_slot: int, offset: int = 0) -> IPv4Address:
    """Return the metallb LB IP at ``${prefix}.255.{slot+offset}``."""
    return IPv4Address(f"{KIND_NETWORK_PREFIX}.255.{cluster_slot + offset}")


def kind_lb_ip_str(cluster_slot: int, offset: int = 0) -> str:
    """String form of :func:`kind_lb_ip`. Convenient for code that builds
    YAML manifests / passes IPs to kubectl as plain strings."""
    return str(kind_lb_ip(cluster_slot, offset))


# Per-cluster slot constants.
KIND_LB_SLOT_OPERATOR = 200  # kind-e2e-operator (and single 'kind')
KIND_LB_SLOT_CLUSTER_1 = 210
KIND_LB_SLOT_CLUSTER_2 = 220
KIND_LB_SLOT_CLUSTER_3 = 230
