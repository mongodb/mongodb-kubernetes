#!/usr/bin/env python3


def pytest_runtest_setup(item):
    """This allows to automatically install the Operator and enable AppDB monitoring before running any test"""
    if "operator_with_monitored_appdb" not in item.fixturenames:
        item.fixturenames.insert(0, "operator_with_monitored_appdb")
