from tests.conftest import default_operator


def pytest_runtest_setup(item):
    """Adds the default_operator fixture in case it is not there already.

    If the test has the "no_operator" fixture, the Operator will not be installed
    but instead it will rely on the currently installed operator. This is handy to
    run local tests."""
    default_operator_name = default_operator.__name__
    if default_operator_name not in item.fixturenames and "no_operator" not in item.fixturenames:
        item.fixturenames.insert(0, default_operator_name)
