import random
import string
import time
from base64 import b64decode
from typing import Any, Callable, Dict, List, Optional

import kubernetes.client
from kubeobject import CustomObject
from kubernetes import client, utils
from kubetester.kubetester import run_periodically

# Re-exports
from .kubetester import fixture as find_fixture
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
    secret = client.V1Secret(metadata=client.V1ObjectMeta(name=name), string_data=data, type=type)

    client.CoreV1Api(api_client=api_client).create_namespaced_secret(namespace, secret)

    return name


def create_or_update_secret(
    namespace: str,
    name: str,
    data: Dict[str, str],
    type: Optional[str] = "Opaque",
    api_client: Optional[client.ApiClient] = None,
) -> str:
    try:
        create_secret(namespace, name, data, type, api_client)
    except kubernetes.client.ApiException as e:
        if e.status == 409:
            update_secret(namespace, name, data, api_client)

    return name


def update_secret(
    namespace: str,
    name: str,
    data: Dict[str, str],
    api_client: Optional[client.ApiClient] = None,
):
    """Updates a secret in a given namespace with the given name and data—handles base64 encoding."""
    secret = client.V1Secret(metadata=client.V1ObjectMeta(name=name), string_data=data)
    client.CoreV1Api(api_client=api_client).patch_namespaced_secret(name, namespace, secret)


def delete_secret(namespace: str, name: str, api_client: Optional[kubernetes.client.ApiClient] = None):
    client.CoreV1Api(api_client=api_client).delete_namespaced_secret(name, namespace)


def create_service_account(namespace: str, name: str) -> str:
    """Creates a service account with `name` in `namespace`"""
    sa = client.V1ServiceAccount(metadata=client.V1ObjectMeta(name=name))
    client.CoreV1Api().create_namespaced_service_account(namespace=namespace, body=sa)
    return name


def delete_service_account(namespace: str, name: str) -> str:
    """Deletes a service account with `name` in `namespace`"""
    client.CoreV1Api().delete_namespaced_service_account(namespace=namespace, name=name)
    return name


def get_service(
    namespace: str, name: str, api_client: Optional[kubernetes.client.ApiClient] = None
) -> client.V1ServiceSpec:
    """Gets a service with `name` in `namespace.
    :return None if the service does not exist
    """
    try:
        return client.CoreV1Api(api_client=api_client).read_namespaced_service(name, namespace)
    except kubernetes.client.ApiException as e:
        if e.status == 404:
            return None
        else:
            raise e


def delete_pvc(namespace: str, name: str):
    """Deletes a persistent volument claim(pvc) with `name` in `namespace`"""
    client.CoreV1Api().delete_namespaced_persistent_volume_claim(namespace=namespace, name=name)


def create_object_from_dict(data, namespace: str) -> List:
    k8s_client = client.ApiClient()
    return utils.create_from_dict(k8s_client=k8s_client, data=data, namespace=namespace)


def read_configmap(namespace: str, name: str, api_client: Optional[client.ApiClient] = None) -> Dict[str, str]:
    return client.CoreV1Api(api_client=api_client).read_namespaced_config_map(name, namespace).data


def create_configmap(
    namespace: str,
    name: str,
    data: Dict[str, str],
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    configmap = client.V1ConfigMap(metadata=client.V1ObjectMeta(name=name), data=data)
    client.CoreV1Api(api_client=api_client).create_namespaced_config_map(namespace, configmap)


def update_configmap(
    namespace: str,
    name: str,
    data: Dict[str, str],
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    configmap = client.V1ConfigMap(metadata=client.V1ObjectMeta(name=name), data=data)
    client.CoreV1Api(api_client=api_client).replace_namespaced_config_map(name, namespace, configmap)


def create_or_update_configmap(
    namespace: str,
    name: str,
    data: Dict[str, str],
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> str:
    try:
        create_configmap(namespace, name, data, api_client)
    except kubernetes.client.ApiException as e:
        if e.status == 409:
            update_configmap(namespace, name, data, api_client)
        else:
            raise Exception(f"failed to create configmap: {e}")

    return name


def create_or_update_service(
    namespace: str,
    service_name: Optional[str] = None,
    cluster_ip: Optional[str] = None,
    ports: Optional[List[client.V1ServicePort]] = None,
    selector=None,
    service: Optional[client.V1Service] = None,
) -> str:
    print("Logging inside create_or_update_service")
    try:
        create_service(namespace, service_name, cluster_ip=cluster_ip, ports=ports, selector=selector, service=service)
    except kubernetes.client.ApiException as e:
        if e.status == 409:
            update_service(
                namespace, service_name, cluster_ip=cluster_ip, ports=ports, selector=selector, service=service
            )
    return service_name


def create_service(
    namespace: str,
    name: str,
    cluster_ip: Optional[str] = None,
    ports: Optional[List[client.V1ServicePort]] = None,
    selector=None,
    service: Optional[client.V1Service] = None,
):
    if service is None:
        if ports is None:
            ports = []

        service = client.V1Service(
            metadata=client.V1ObjectMeta(name=name, namespace=namespace),
            spec=client.V1ServiceSpec(ports=ports, cluster_ip=cluster_ip, selector=selector),
        )
    client.CoreV1Api().create_namespaced_service(namespace, service)


def update_service(
    namespace: str,
    name: str,
    cluster_ip: Optional[str] = None,
    ports: Optional[List[client.V1ServicePort]] = None,
    selector=None,
    service: Optional[client.V1Service] = None,
):
    if service is None:
        if ports is None:
            ports = []

        service = client.V1Service(
            metadata=client.V1ObjectMeta(name=name, namespace=namespace),
            spec=client.V1ServiceSpec(ports=ports, cluster_ip=cluster_ip, selector=selector),
        )
    client.CoreV1Api().patch_namespaced_service(name, namespace, service)


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


def read_service(
    namespace: str,
    name: str,
    api_client: Optional[client.ApiClient] = None,
) -> client.V1Service:
    return client.CoreV1Api(api_client=api_client).read_namespaced_service(name, namespace)


def read_secret(
    namespace: str,
    name: str,
    api_client: Optional[client.ApiClient] = None,
) -> Dict[str, str]:
    return decode_secret(client.CoreV1Api(api_client=api_client).read_namespaced_secret(name, namespace).data)


def delete_pod(namespace: str, name: str, api_client: Optional[kubernetes.client.ApiClient] = None):
    client.CoreV1Api(api_client=api_client).delete_namespaced_pod(name, namespace)


def create_or_update_namespace(
    namespace: str,
    labels: dict = None,
    annotations: dict = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    namespace_resource = client.V1Namespace(
        metadata=client.V1ObjectMeta(
            name=namespace,
            labels=labels,
            annotations=annotations,
        )
    )
    try:
        client.CoreV1Api(api_client=api_client).create_namespace(namespace_resource)
    except kubernetes.client.ApiException as e:
        if e.status == 409:
            client.CoreV1Api(api_client=api_client).patch_namespace(namespace, namespace_resource)


def delete_namespace(name: str):
    c = client.CoreV1Api()
    c.delete_namespace(name, body=c.V1DeleteOptions())


def get_deployments(namespace: str):
    return client.AppsV1Api().list_namespaced_deployment(namespace)


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


def get_statefulset(
    namespace: str,
    name: str,
    api_client: Optional[client.ApiClient] = None,
) -> client.V1StatefulSet:
    return client.AppsV1Api(api_client=api_client).read_namespaced_stateful_set(name, namespace)


def statefulset_is_deleted(namespace: str, name: str, api_client: Optional[client.ApiClient]):
    try:
        get_statefulset(namespace, name, api_client=api_client)
        return False
    except client.ApiException as e:
        if e.status == 404:
            return True
        else:
            raise e


def delete_cluster_role(name: str, api_client: Optional[client.ApiClient] = None):
    try:
        client.RbacAuthorizationV1Api(api_client=api_client).delete_cluster_role(name)
    except client.rest.ApiException as e:
        if e.status != 404:
            raise e


def delete_cluster_role_binding(name: str, api_client: Optional[client.ApiClient] = None):
    try:
        client.RbacAuthorizationV1Api(api_client=api_client).delete_cluster_role_binding(name)
    except client.rest.ApiException as e:
        if e.status != 404:
            raise e


def random_k8s_name(prefix=""):
    return prefix + "".join(random.choice(string.ascii_lowercase) for _ in range(10))


def get_pod_when_running(
    namespace: str,
    label_selector: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> client.V1Pod:
    """
    Returns a Pod that matches label_selector. It will block until the Pod is in
    Running state.
    """
    while True:
        time.sleep(3)

        try:
            pods = client.CoreV1Api(api_client=api_client).list_namespaced_pod(namespace, label_selector=label_selector)
            try:
                pod = pods.items[0]
            except IndexError:
                continue

            if pod.status.phase == "Running":
                return pod

        except client.rest.ApiException as e:
            # The Pod might not exist in Kubernetes yet so skip any 404
            if e.status != 404:
                raise


def get_pod_when_ready(
    namespace: str,
    label_selector: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
    default_retry: Optional[int] = 60,
) -> client.V1Pod:
    """
    Returns a Pod that matches label_selector. It will block until the Pod is in
    Ready state.
    """
    cnt = 0

    while True and cnt < default_retry:
        print(f"get_pod_when_ready: namespace={namespace}, label_selector={label_selector}")

        if cnt > 0:
            time.sleep(1)
        cnt += 1
        try:
            pods = client.CoreV1Api(api_client=api_client).list_namespaced_pod(namespace, label_selector=label_selector)

            if len(pods.items) == 0:
                continue

            pod = pods.items[0]

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

    print(f"bailed on getting pod ready after 10 retries")


def is_pod_ready(
    namespace: str,
    label_selector: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> client.V1Pod:
    """
    Checks if a Pod that matches label_selector is ready. It will return False if the pod is not ready,
    if it does not exist or there is any other kind of error.
    This function is intended to check if installing third party components is needed.
    """
    print(f"Checking if pod is ready: namespace={namespace}, label_selector={label_selector}")
    try:
        pods = client.CoreV1Api(api_client=api_client).list_namespaced_pod(namespace, label_selector=label_selector)

        if len(pods.items) == 0:
            return None

        pod = pods.items[0]

        if pod.status.conditions is None:
            return None

        for condition in pod.status.conditions:
            if condition.type == "Ready" and condition.status == "True":
                return pod
    except client.rest.ApiException:
        return None

    return None


def get_default_storage_class() -> str:
    default_class_annotations = (
        "storageclass.kubernetes.io/is-default-class",  # storage.k8s.io/v1
        "storageclass.beta.kubernetes.io/is-default-class",  # storage.k8s.io/v1beta1
    )
    sc: client.V1StorageClass
    for sc in client.StorageV1Api().list_storage_class().items:
        if sc.metadata.annotations is not None and any(
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


def try_load(resource: CustomObject) -> bool:
    """
    Tries to load the resource without raising an exception when the resource does not exist.
    Returns False if the resource does not exist.
    """
    try:
        resource.load()
    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise e
        else:
            return False

    return True


def wait_for_webhook(namespace, retries=5, delay=5, service_name="operator-webhook"):
    webhook_api = client.AdmissionregistrationV1Api()
    core_api = client.CoreV1Api()

    for attempt in range(retries):
        try:
            core_api.read_namespaced_service(service_name, namespace)

            # make sure the validating_webhook is installed.
            webhook_api.read_validating_webhook_configuration("mdbpolicy.mongodb.com")
            print("Webhook is ready.")
            return True
        except kubernetes.client.ApiException as e:
            print(f"Attempt {attempt + 1} failed, webhook not ready. Sleeping: {delay}, error: {e}")
            time.sleep(delay)

    print("Webhook did not become ready in time.")
    return False
