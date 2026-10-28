"""The MetalLB pools of the kind clusters, as the tests see them."""

import os
from ipaddress import IPv4Address


def kind_lb_ip(pool: str, offset: int = 0) -> str:
    """Address `offset` above the first one of the pool in KIND_METALLB_RANGE_<pool> ("<first>-<last>")."""
    name = f"KIND_METALLB_RANGE_{pool}"
    pool_range = os.environ.get(name)
    if not pool_range:
        raise RuntimeError(
            f"{name} is not set: it comes from scripts/dev/contexts/root-context (make switch), "
            "in the test pod from the test-app chart (single_e2e.sh)"
        )
    first, last = (IPv4Address(address) for address in pool_range.split("-"))
    if first + offset > last:
        raise ValueError(f"offset {offset} is outside {name}={pool_range}")
    return str(first + offset)
