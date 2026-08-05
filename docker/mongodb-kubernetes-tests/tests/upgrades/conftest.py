from kubetester import downgrade_pss_to_warn
from pytest import fixture


@fixture(scope="module")
def namespace(namespace: str) -> str:
    """Downgrade PSS from enforce to warn for upgrade tests.

    Upgrade tests install released operator versions that predate PSS-restricted
    compliance and therefore cannot run under enforce mode.
    """
    downgrade_pss_to_warn(namespace)
    return namespace
