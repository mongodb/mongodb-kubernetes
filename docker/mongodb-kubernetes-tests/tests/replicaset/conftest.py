from typing import Optional

from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.common.ops_manager.om_deployment import get_ops_manager
from tests.conftest import local_operator


def pytest_runtest_setup(item):
    """This allows to automatically install the default Operator before running any test.

    When the operator runs locally from source (LOCAL_OPERATOR=true) the installation step would
    only perform a redundant helm upgrade against the operator release already in use, so it is
    skipped; tests that need a specific operator still request the fixture explicitly.
    """
    if "default_operator" not in item.fixturenames and not local_operator():
        item.fixturenames.insert(0, "default_operator")


@fixture(scope="module")
def ops_manager(namespace: str) -> Optional[MongoDBOpsManager]:
    """The in-cluster Ops Manager to configure MongoDB resources against.

    Returns None in cloud_manager mode (external cloud-QA Ops Manager, wired via the my-project
    ConfigMap/my-credentials Secret) and in headless mode (no Ops Manager at all); in ops_manager
    mode it creates (or loads) the resource and waits for it to be usable.
    """
    om = get_ops_manager(namespace)
    if om is not None:
        om.update()
        om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    return om
