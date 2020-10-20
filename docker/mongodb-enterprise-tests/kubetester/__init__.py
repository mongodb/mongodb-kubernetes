import random
import string
import time
from base64 import b64decode
from typing import Dict, Optional

from kubernetes import client
from kubernetes.client.rest import ApiException

# Re-exports
from .kubetester import fixture as find_fixture
from .mongodb import MongoDB
from .security_context import (
    assert_pod_container_security_context,
    assert_pod_security_context,
)


def create_secret(
    namespace: str, name: str, data: Dict[str, str], type: Optional[str] = "Opaque"
) -> str:
    """Creates a Secret with `name` in `namespace`. String contents are passed as the `data` parameter."""
    secret = client.V1Secret(
        metadata=client.V1ObjectMeta(name=name), string_data=data, type=type
    )
    client.CoreV1Api().create_namespaced_secret(namespace, secret)

    return name


def read_secret(namespace: str, name: str) -> Dict[str, str]:
    return decode_secret(
        client.CoreV1Api().read_namespaced_secret(name, namespace).data
    )


def delete_secret(namespace: str, name: str):
    client.CoreV1Api().delete_namespaced_secret(name, namespace)


def random_k8s_name(prefix=""):
    return prefix + "".join(random.choice(string.ascii_lowercase) for _ in range(10))


def get_pod_when_ready(namespace: str, label_selector: str) -> client.V1Pod:
    """Returns a Pod that matches label_selector. It will block until the Pod is in
    Ready state.

    """
    while True:
        time.sleep(3)

        try:
            pods = client.CoreV1Api().list_namespaced_pod(
                namespace, label_selector=label_selector
            )
            try:
                pod = pods.items[0]
            except IndexError:
                continue

            for condition in pod.status.conditions:
                if condition.type == "Ready" and condition.status == "True":
                    return pod

        except client.rest.ApiException as e:
            # The Pod might not exist in Kubernetes yet so skip any 404
            if e.status != 404:
                raise


def decode_secret(data: Dict[str, str]) -> Dict[str, str]:
    return {k: b64decode(v).decode("utf-8") for (k, v) in data.items()}
