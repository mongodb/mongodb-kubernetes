from kubetester import downgrade_pss_to_warn
from pytest import fixture


@fixture(scope="module")
def namespace(namespace: str) -> str:
    """Downgrade PSS from enforce to warn for OLM upgrade tests.

    These tests install released operator versions that predate PSS-restricted
    compliance and therefore cannot run under enforce mode.

    TODO: remove once 1.9.0 is released — from that version the operator Helm
    chart includes the required securityContext fields and will pass enforcement.
    """
    downgrade_pss_to_warn(namespace)
    return namespace
