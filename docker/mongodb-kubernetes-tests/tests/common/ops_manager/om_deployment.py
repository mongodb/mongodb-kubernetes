from typing import Optional

from kubetester import try_load
from kubetester.kubetester import PROJECT_MODE_OPS_MANAGER, get_project_mode, is_multi_cluster
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import get_custom_appdb_version, get_custom_om_version
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


def get_ops_manager(namespace: str) -> Optional[MongoDBOpsManager]:
    """Returns the in-cluster Ops Manager resource for the current project mode.

    MDB_E2E_PROJECT_MODE=ops_manager gets a MongoDBOpsManager (created by the caller);
    cloud_manager (external cloud-QA) and headless (no Ops Manager at all) both return None and
    rely on MongoDB.configure(om=None, ...) to pick the matching connection settings.
    """
    if get_project_mode() != PROJECT_MODE_OPS_MANAGER:
        return None

    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )

    resource.set_version(get_custom_om_version())
    resource.set_appdb_version(get_custom_appdb_version())

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource
