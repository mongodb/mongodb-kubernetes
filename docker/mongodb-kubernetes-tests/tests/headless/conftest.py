"""Fixtures for the headless MongoDB tests."""

from pytest import fixture


@fixture(scope="module", autouse=True)
def operator_installed(default_operator):
    """Ensure the operator under test is installed.

    CI starts the task without a running operator and the headless tests do not otherwise request
    the default install fixture. Locally the operator runs from source, in which case the fixture
    installs a zero-replica deployment (LOCAL_OPERATOR=true) and does not disturb it.
    """
    return default_operator
