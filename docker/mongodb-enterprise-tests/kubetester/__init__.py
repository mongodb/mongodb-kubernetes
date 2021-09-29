import random
import string
import time
from base64 import b64decode
from typing import Any, Callable, Dict, List, Optional

import kubernetes.client
from kubernetes import client, utils

from kubetester.kubetester import run_periodically

# Re-exports
from .kubetester import fixture as find_fixture
from .mongodb import MongoDB
from .security_context import (
    assert_pod_container_security_context,
    assert_pod_security_context,
)


def create_secret(
    namespace: str,
    name: str,
    data: Dict[str, str],
    type: Optional[str] = "Opaque",
    api_client: Optional[client.ApiClient] = None,
) -> str:
    """Creates a Secret with `name` in `namespace`. String contents are passed as the `data` parameter."""
    secret = client.V1Secret(
        metadata=client.V1ObjectMeta(name=name), string_data=data, type=type
    )
    client.CoreV1Api(api_client=api_client).create_namespaced_secret(namespace, secret)

    return name


def create_service_account(namespace: str, name: str) -> str:
    """Creates a service account with `name` in `namespace`"""
    sa = client.V1ServiceAccount(metadata=client.V1ObjectMeta(name=name))
    client.CoreV1Api().create_namespaced_service_account(namespace=namespace, body=sa)
    return name


def delete_service_account(namespace: str, name: str) -> str:
    """Deletes a service account with `name` in `namespace`"""
    sa = client.V1ServiceAccount(metadata=client.V1ObjectMeta(name=name))
    client.CoreV1Api().delete_namespaced_service_account(namespace=namespace, name=name)
    return name


def delete_pvc(namespace: str, name: str):
    """Deletes a persistent volument claim(pvc) with `name` in `namespace`"""
    pvc = client.V1PersistentVolumeClaim(metadata=client.V1ObjectMeta(name=name))
    client.CoreV1Api().delete_namespaced_persistent_volume_claim(
        namespace=namespace, name=name
    )


def create_object_from_dict(data, namespace: str) -> List:
    k8s_client = client.ApiClient()
    return utils.create_from_dict(k8s_client=k8s_client, data=data, namespace=namespace)


def create_configmap(
    namespace: str,
    name: str,
    data: Dict[str, str],
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    configmap = client.V1ConfigMap(metadata=client.V1ObjectMeta(name=name), data=data)
    client.CoreV1Api(api_client=api_client).create_namespaced_config_map(
        namespace, configmap
    )


def create_service(
    namespace: str,
    name: str,
    cluster_ip: Optional[str] = None,
    ports: Optional[List[client.V1ServicePort]] = None,
):
    if ports is None:
        ports = []

    service = client.V1Service(
        metadata=client.V1ObjectMeta(name=name, namespace=namespace),
        spec=client.V1ServiceSpec(ports=ports, cluster_ip=cluster_ip),
    )
    client.CoreV1Api().create_namespaced_service(namespace, service)


def create_statefulset(
    namespace: str,
    name: str,
    service_name: str,
    labels: Dict[str, str],
    replicas: int = 1,
    containers: Optional[List[client.V1Container]] = None,
    volumes: Optional[List[client.V1Volume]] = None,
):
    if containers is None:
        containers = []
    if volumes is None:
        volumes = []

    sts = client.V1StatefulSet(
        metadata=client.V1ObjectMeta(name=name, namespace=namespace),
        spec=client.V1StatefulSetSpec(
            selector=client.V1LabelSelector(match_labels=labels),
            replicas=replicas,
            service_name=service_name,
            template=client.V1PodTemplateSpec(
                metadata=client.V1ObjectMeta(labels=labels),
                spec=client.V1PodSpec(containers=containers, volumes=volumes),
            ),
        ),
    )
    client.AppsV1Api().create_namespaced_stateful_set(namespace, body=sts)


def read_secret(
    namespace: str,
    name: str,
    api_client: Optional[client.ApiClient] = None,
) -> Dict[str, str]:
    return decode_secret(
        client.CoreV1Api(api_client=api_client)
        .read_namespaced_secret(name, namespace)
        .data
    )


def delete_secret(namespace: str, name: str):
    client.CoreV1Api().delete_namespaced_secret(name, namespace)


def delete_pod(namespace: str, name: str):
    client.CoreV1Api().delete_namespaced_pod(name, namespace)


def delete_namespace(name: str):
    c = client.CoreV1Api()
    c.delete_namespace(name, body=c.V1DeleteOptions())


def delete_deployment(namespace: str, name: str):
    client.AppsV1Api().delete_namespaced_deployment(name, namespace)


def delete_statefulset(
    namespace: str,
    name: str,
    propagation_policy: str = "Orphan",
    api_client: Optional[client.ApiClient] = None,
):
    client.AppsV1Api(api_client=api_client).delete_namespaced_stateful_set(
        name, namespace, propagation_policy=propagation_policy
    )


def random_k8s_name(prefix=""):
    return prefix + "".join(random.choice(string.ascii_lowercase) for _ in range(10))


def get_pod_when_ready(
    namespace: str,
    label_selector: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> client.V1Pod:
    """Returns a Pod that matches label_selector. It will block until the Pod is in
    Ready state.

    """
    while True:
        time.sleep(3)

        try:
            pods = client.CoreV1Api(api_client=api_client).list_namespaced_pod(
                namespace, label_selector=label_selector
            )
            try:
                pod = pods.items[0]
            except IndexError:
                continue

            # This might happen when the pod is still pending
            if pod.status.conditions is None:
                continue

            for condition in pod.status.conditions:
                if condition.type == "Ready" and condition.status == "True":
                    return pod

        except client.rest.ApiException as e:
            # The Pod might not exist in Kubernetes yet so skip any 404
            if e.status != 404:
                raise


def get_default_storage_class() -> str:
    default_class_annotations = (
        "storageclass.kubernetes.io/is-default-class",  # storage.k8s.io/v1
        "storageclass.beta.kubernetes.io/is-default-class",  # storage.k8s.io/v1beta1
    )
    sc: client.V1StorageClass
    for sc in client.StorageV1Api().list_storage_class().items:
        if any(
            sc.metadata.annotations.get(a) == "true" for a in default_class_annotations
        ):
            return sc.metadata.name


def decode_secret(data: Dict[str, str]) -> Dict[str, str]:
    return {k: b64decode(v).decode("utf-8") for (k, v) in data.items()}


def wait_until(fn: Callable[..., Any], timeout=0, **kwargs):
    """
    Runs the Callable `fn` until timeout is reached or until it returns True.
    """
    return run_periodically(fn, timeout=timeout, **kwargs)
