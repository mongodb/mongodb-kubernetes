from kubetester import downgrade_pss_to_warn
from pytest import fixture


@fixture(scope="module")
def namespace(namespace: str) -> str:
    """Downgrade PSS from enforce to warn for no-mesh multicluster OM tests.

    These tests deploy a third-party nginx interconnect (macbre/nginx-http3) that
    writes to root-owned paths at startup and cannot run as non-root without
    significant image changes. The namespace is downgraded to warn so the nginx
    pod can start while PSS violations are still surfaced as warnings.
    """
    downgrade_pss_to_warn(namespace)
    return namespace
