import os

from pytest import fixture

import kubernetes


try:
    kubernetes.config.load_kube_config()
except Exception:
    kubernetes.config.load_incluster_config()


@fixture(scope="module")
def namespace() -> str:
    namespace = os.getenv("PROJECT_NAMESPACE", None)

    if namespace is None:
        raise Exception("PROJECT_NAMESPACE needs to be defined")

    return namespace
