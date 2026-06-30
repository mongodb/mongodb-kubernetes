import pytest
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

EXT_APPDB_SECRET_NAME = "primary-om-db-ext-connection-string"
EXT_APPDB_SECRET_KEY = "connectionString.standard"


@fixture(scope="module")
def meta_om(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_mdb(primary_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="canary-rs",
    )
    resource.configure(primary_om, "development")
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om_external_appdb(meta_om: MongoDBOpsManager, primary_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=primary_om.app_db_name(),
    )
    resource.configure(meta_om, "appdb-project")
    resource.set_version(primary_om["spec"]["applicationDatabase"]["version"])
    try_load(resource)
    return resource
