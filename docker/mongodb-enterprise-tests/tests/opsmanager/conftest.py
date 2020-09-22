#!/usr/bin/env python3

import os

from pytest import fixture


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
    defaults to custom_mdb_version() (in most cases we need to use the same version for MongoDB as for AppDB) """
    # TODO uncomment when AppDB 4.4 is supported
    return "4.2.8"
    # return os.getenv("CUSTOM_APPDB_VERSION", custom_mdb_version)


def ensure_ent_version(mdb_version: str) -> str:
    if "-ent" not in mdb_version:
        return mdb_version + "-ent"
    return mdb_version
