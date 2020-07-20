import time

from kubernetes import client

from .mongodb import MongoDB
from .certs import Certificate


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
