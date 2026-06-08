from kubetester import label_namespace
from pytest import fixture


@fixture(scope="module")
def namespace(namespace: str) -> str:
    """Downgrade PSS from enforce to warn for upgrade tests.

    Upgrade tests install released operator versions that predate PSS-restricted
    compliance and therefore cannot run under enforce mode.
    """
    label_namespace(
        namespace,
        {
            "pod-security.kubernetes.io/enforce": None,
            "pod-security.kubernetes.io/warn": "restricted",
        },
    )
    return namespace
