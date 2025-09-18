from typing import Optional

from pytest import fixture

from kubetester import try_load
from kubetester.opsmanager import MongoDBOpsManager
from tests.common.ops_manager.cloud_manager import is_cloud_qa
from kubetester.kubetester import fixture as yaml_fixture, is_multi_cluster
from tests.conftest import get_custom_om_version, get_custom_appdb_version
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


def get_ops_manager(namespace: str) -> Optional[MongoDBOpsManager]:
    if is_cloud_qa():
        return None

    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )

    if try_load(resource):
        return resource

    resource.set_version(get_custom_om_version())
    resource.set_appdb_version(get_custom_appdb_version())
    resource.allow_mdb_rc_versions()

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource
