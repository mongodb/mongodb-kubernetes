def pytest_runtest_setup(item):
    """ This removes the default operator fixture for all the tests in the current directory """
    if "default_operator" in item.fixturenames:
        item.fixturenames.remove("default_operator")
