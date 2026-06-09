from kubetester import label_namespace
from pytest import fixture


@fixture(scope="module")
def namespace(namespace: str) -> str:
    """Downgrade PSS from enforce to warn for no-mesh multicluster OM tests.

    These tests deploy a third-party nginx interconnect (macbre/nginx-http3) that
    writes to root-owned paths at startup and cannot run as non-root without
    significant image changes. The namespace is downgraded to warn so the nginx
    pod can start while PSS violations are still surfaced as warnings.
    """
    label_namespace(
        namespace,
        {
            "pod-security.kubernetes.io/enforce": None,
            "pod-security.kubernetes.io/warn": "restricted",
        },
    )
    return namespace
