#!/usr/bin/env python3

import os

from pytest import fixture, skip
from kubetester.opsmanager import MongoDBOpsManager


def pytest_runtest_setup(item):
    """ This allows to automatically install the default Operator before running any test """
    if "default_operator" not in item.fixturenames:
        item.fixturenames.insert(0, "default_operator")


@fixture(scope="module")
def custom_mdb_prev_version() -> str:
    """Returns a CUSTOM_MDB_PREV_VERSION for Mongodb to be created/upgraded to for testing.
    Used for backup mainly (to test backup for different mdb versions).
    Defaults to 4.2.8 (simplifies testing locally)"""
    return os.getenv("CUSTOM_MDB_PREV_VERSION", "4.2.8")


@fixture(scope="module")
def custom_appdb_version() -> str:
    """Returns a CUSTOM_APPDB_VERSION for AppDB to be created/upgraded to for testing,
    defaults to custom_mdb_version() (in most cases we need to use the same version for MongoDB as for AppDB)"""

    # Defaulting to 4.4.0-ent if not specified as we didn't release all possible images yet
    return os.getenv("CUSTOM_APPDB_VERSION", "4.4.0-ent")


def ensure_ent_version(mdb_version: str) -> str:
    if "-ent" not in mdb_version:
        return mdb_version + "-ent"
    return mdb_version


@fixture(scope="module")
def gen_key_resource_version(ops_manager: MongoDBOpsManager) -> str:
    secret = ops_manager.read_gen_key_secret()
    return secret.metadata.resource_version


@fixture(scope="module")
def admin_key_resource_version(ops_manager: MongoDBOpsManager) -> str:
    secret = ops_manager.read_api_key_secret()
    return secret.metadata.resource_version


@fixture
def skip_if_om5(custom_version: str):
    """
    When including this fixture on a test, the test will be skipped,
    if the "custom_version" is set to OM5.0
    """
    if custom_version.startswith("5."):
        raise skip("Skipping on OM5.0 tests")
