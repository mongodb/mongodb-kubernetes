import logging

# Suppress DEBUG noise from low-level HTTP/k8s libraries — their response bodies
# clutter the live log output without adding diagnostic value.
logging.getLogger("kubernetes.client.rest").setLevel(logging.WARNING)
logging.getLogger("botocore").setLevel(logging.WARNING)
logging.getLogger("urllib3").setLevel(logging.WARNING)


def pytest_runtest_setup(item):
    """This allows to automatically install the default Operator before running any test"""
    if "default_operator" not in item.fixturenames:
        item.fixturenames.insert(0, "default_operator")
