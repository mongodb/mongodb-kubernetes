import random
import string
import time
from base64 import b64decode
from typing import Any, Callable, Dict, List, Optional

import kubernetes.client
import requests
from kubeobject import CustomObject
from kubernetes import client, utils
from kubetester.kubetester import run_periodically
from tests import test_logger

# Re-exports
from .kubetester import fixture as find_fixture
from .security_context import assert_pod_container_security_context, assert_pod_security_context

logger = test_logger.get_test_logger(__name__)


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
) -> Optional[client.V1Service]:
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
    if service_name is None and service is not None:
        if isinstance(service, dict):
            service_name = service.get("metadata", {}).get("name")
        elif hasattr(service, "metadata") and service.metadata is not None:
            service_name = service.metadata.name
    if service_name is None:
        raise ValueError("service_name must not be None")
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
    labels: Optional[dict] = None,
    annotations: Optional[dict] = None,
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


def label_namespace(name: str, labels: dict):
    body = {"metadata": {"labels": labels}}
    client.CoreV1Api().patch_namespace(name, body)


def downgrade_pss_to_warn(namespace: str) -> None:
    """Downgrade a namespace from PSS enforce to warn mode.

    Used for test namespaces that contain third-party or legacy components that
    predate PSS-restricted compliance (e.g. old operator releases, nginx images
    that run as root). PSS violations are still surfaced as warnings.
    """
    label_namespace(
        namespace,
        {
            "pod-security.kubernetes.io/enforce": None,
            "pod-security.kubernetes.io/warn": "restricted",
        },
    )


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


def scale_statefulset(
    namespace: str,
    name: str,
    replicas: int,
    api_client: Optional[client.ApiClient] = None,
) -> None:
    client.AppsV1Api(api_client=api_client).patch_namespaced_stateful_set(
        name, namespace, {"spec": {"replicas": replicas}}
    )


def wait_for_statefulset_replicas(
    namespace: str,
    name: str,
    replicas: int,
    api_client: Optional[client.ApiClient] = None,
    timeout: int = 120,
):
    def statefulset_has_replicas() -> bool:
        return get_statefulset(namespace, name, api_client=api_client).spec.replicas == replicas

    wait_until(statefulset_has_replicas, timeout=timeout)


def statefulset_is_deleted(namespace: str, name: str, api_client: Optional[client.ApiClient]):
    try:
        get_statefulset(namespace, name, api_client=api_client)
        return False
    except client.ApiException as e:
        if e.status == 404:
            return True
        else:
            raise e


def wait_for_statefulset_recreated(
    namespace: str,
    name: str,
    old_uid: str,
    api_client: Optional[client.ApiClient] = None,
    timeout: int = 120,
):
    def statefulset_is_recreated() -> bool:
        try:
            return get_statefulset(namespace, name, api_client=api_client).metadata.uid != old_uid
        except client.ApiException as e:
            if e.status == 404:
                return False
            raise e

    wait_until(statefulset_is_recreated, timeout=timeout)


def wait_for_statefulset_ready(
    namespace: str,
    name: str,
    api_client: Optional[client.ApiClient] = None,
    timeout: int = 600,
):
    def statefulset_is_ready() -> bool:
        sts = get_statefulset(namespace, name, api_client=api_client)
        wanted = sts.spec.replicas
        return (
            sts.status.observed_generation == sts.metadata.generation
            and (sts.status.updated_replicas or 0) == wanted
            and (sts.status.ready_replicas or 0) == wanted
            and (sts.status.replicas or 0) == wanted
        )

    wait_until(statefulset_is_ready, timeout=timeout)


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
    timeout: int = 600,
) -> client.V1Pod:
    """
    Returns a Pod that matches label_selector. It will block until the Pod is in
    Running state or timeout is reached.
    """
    deadline = time.time() + timeout
    while time.time() < deadline:
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

    raise Exception(
        f"Timeout ({timeout}s) waiting for pod with label_selector '{label_selector}' in namespace '{namespace}' to be Running"
    )


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

    while default_retry is not None and cnt < default_retry:
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


def list_matching_pods(
    namespace: str,
    *,
    label_selector: Optional[str] = None,
    name_prefix: Optional[str] = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> List[client.V1Pod]:
    """List pods in ``namespace`` filtered by label selector and/or name prefix.

    At least one of ``label_selector`` / ``name_prefix`` must be set.
    """
    if not label_selector and not name_prefix:
        raise ValueError("must provide label_selector or name_prefix")
    kwargs: Dict[str, Any] = {}
    if label_selector:
        kwargs["label_selector"] = label_selector
    pods = client.CoreV1Api(api_client=api_client).list_namespaced_pod(namespace, **kwargs).items
    if name_prefix:
        pods = [p for p in pods if p.metadata.name.startswith(name_prefix)]
    return pods


def pod_is_ready(pod: client.V1Pod) -> bool:
    conds = pod.status.conditions or []
    return any(c.type == "Ready" and c.status == "True" for c in conds)


def wait_for_pods_ready(
    namespace: str,
    *,
    label_selector: Optional[str] = None,
    name_prefix: Optional[str] = None,
    expected_count: Optional[int] = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
    timeout: int = 300,
    sleep_time: int = 3,
) -> List[client.V1Pod]:
    """Block until at least ``expected_count`` matching pods report Ready=True.

    When ``expected_count`` is ``None``, every matching pod must be Ready and
    there must be at least one. Returns the Ready pod list on success.
    """

    def check() -> tuple:
        pods = list_matching_pods(
            namespace,
            label_selector=label_selector,
            name_prefix=name_prefix,
            api_client=api_client,
        )
        if not pods:
            return False, "no pods"
        ready = [p for p in pods if pod_is_ready(p)]
        want = expected_count if expected_count is not None else len(pods)
        return len(ready) >= want, f"ready={len(ready)}/{len(pods)} want={want}"

    selector = label_selector or f"name~{name_prefix}*"
    run_periodically(
        check,
        timeout=timeout,
        sleep_time=sleep_time,
        msg=f"pods Ready=True selector={selector}",
    )
    return [
        p
        for p in list_matching_pods(
            namespace,
            label_selector=label_selector,
            name_prefix=name_prefix,
            api_client=api_client,
        )
        if pod_is_ready(p)
    ]


def wait_for_no_pods_ready(
    namespace: str,
    *,
    label_selector: Optional[str] = None,
    name_prefix: Optional[str] = None,
    expected_not_ready: Optional[int] = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
    timeout: int = 180,
    sleep_time: int = 3,
) -> None:
    """Block until no matching pod has Ready=True (or no pods match at all).

    When ``expected_not_ready`` is given, at least that many matching pods
    must report Ready=False (the rest may be Ready or absent).
    """

    def check() -> tuple:
        pods = list_matching_pods(
            namespace,
            label_selector=label_selector,
            name_prefix=name_prefix,
            api_client=api_client,
        )
        if not pods:
            return True, "no pods"
        not_ready = sum(1 for p in pods if not pod_is_ready(p))
        want = expected_not_ready if expected_not_ready is not None else len(pods)
        return not_ready >= want, f"not_ready={not_ready}/{len(pods)} want={want}"

    selector = label_selector or f"name~{name_prefix}*"
    run_periodically(
        check,
        timeout=timeout,
        sleep_time=sleep_time,
        msg=f"pods Ready=False selector={selector}",
    )


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


def get_default_storage_class() -> Optional[str]:
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
    return None


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


def wait_for_webhook(
    namespace,
    retries: int = 10,
    multi_cluster: bool = False,
    service_name="operator-webhook",
    validation_endpoint: str = "validate-mongodb-com-v1-mongodb",
):
    from tests.conftest import get_central_cluster_name, get_cluster_domain, get_test_pod_cluster_name, local_operator

    # we don't want to wait for the operator webhook if the operator is running locally and not in a pod
    if local_operator():
        return

    # in multi-cluster mode the operator and the test pod are in different clusters(test pod won't be able to talk to webhook),
    # so we skip this extra check for multi-cluster
    if multi_cluster and get_central_cluster_name() != get_test_pod_cluster_name():
        logger.info(
            f"Skipping waiting for the webhook as we cannot call the webhook endpoint from a test_pod_cluster ({get_test_pod_cluster_name()}) "
            f"to central cluster ({get_central_cluster_name()}); sleeping for 10s instead"
        )
        # We need to sleep here otherwise the function returns too early and we create a race condition in tests
        time.sleep(10)
        return

    webhook_services = client.CoreV1Api().list_namespaced_service(namespace)
    logger.debug("Listing webhook services...")
    for svc in webhook_services.items:
        if "webhook" in svc.metadata.name:
            logger.debug(
                f"Service: {svc.metadata.name}, ClusterIP: {svc.spec.cluster_ip}, Ports: {svc.spec.ports}, Selector: {svc.spec.selector}"
            )

    logger.debug("wait_for_webhook")
    webhook_endpoint = "https://{}.{}.svc.{}/{}".format(
        service_name, namespace, get_cluster_domain(), validation_endpoint
    )
    headers = {"Content-Type": "application/json"}
    logger.debug(f"Webhook_endpoint: {webhook_endpoint}")
    retry_count = retries + 1
    while retry_count > 0:
        retry_count -= 1
        logger.debug("Waiting for operator/webhook to be functional")
        try:
            response = requests.post(webhook_endpoint, headers=headers, verify=False, timeout=2)
        except Exception as e:
            logger.warning(e)
            time.sleep(2)
            continue

        try:
            # Let's assume that if we get a json response, then the webhook
            # is already in place.
            response.json()
        except Exception:
            logger.warning("Didn't get a json response from webhook")
        else:
            return
        time.sleep(2)

    raise Exception("Operator webhook didn't start after {} retries".format(retries))
