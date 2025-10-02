#!/usr/bin/env python3

import argparse
import os
import sys
from typing import Dict

import k8s_conditions
import yaml
from kubernetes import client, config
from kubernetes.client.rest import ApiException

TEST_POD_NAME = "e2e-test"
TEST_CLUSTER_ROLE_NAME = "e2e-test"
TEST_CLUSTER_ROLE_BINDING_NAME = "e2e-test"
TEST_SERVICE_ACCOUNT_NAME = "e2e-test"


def load_yaml_from_file(path: str) -> Dict:
    with open(path, "r") as f:
        return yaml.full_load(f.read())


def _load_test_service_account() -> Dict:
    return load_yaml_from_file("mongodb-community-operator/deploy/e2e/service_account.yaml")


def _load_test_role() -> Dict:
    return load_yaml_from_file("mongodb-community-operator/deploy/e2e/role.yaml")


def _load_test_role_binding() -> Dict:
    return load_yaml_from_file("mongodb-community-operator/deploy/e2e/role_binding.yaml")


def _prepare_test_environment(namespace) -> None:
    """
    _prepare_test_environment ensures that the old test pod is deleted
    and that namespace, cluster role, cluster role binding and service account
    are created for the test pod.
    """
    rbacv1 = client.RbacAuthorizationV1Api()
    corev1 = client.CoreV1Api()
    _delete_test_pod(namespace)

    print("Creating Namespace")
    k8s_conditions.ignore_if_already_exists(
        lambda: corev1.create_namespace(
            client.V1Namespace(metadata=dict(name=namespace, labels={"pod-security.kubernetes.io/warn": "restricted"}))
        )
    )

    print("Creating Cluster Role Binding and Service Account for test pod")
    role_binding = _load_test_role_binding()
    # set namespace specified in config.json
    role_binding["subjects"][0]["namespace"] = namespace

    k8s_conditions.ignore_if_already_exists(lambda: rbacv1.create_cluster_role_binding(role_binding))

    print("Creating Service Account for test pod")
    service_account = _load_test_service_account()
    # set namespace specified in config.json
    service_account["metadata"]["namespace"] = namespace

    k8s_conditions.ignore_if_already_exists(
        lambda: corev1.create_namespaced_service_account(namespace, service_account)
    )


def create_test_pod(args: argparse.Namespace, namespace: str) -> None:
    corev1 = client.CoreV1Api()
    test_pod = {
        "kind": "Pod",
        "metadata": {
            "name": TEST_POD_NAME,
            "namespace": namespace,
            "labels": {"e2e-test": "true"},
        },
        "spec": {
            "restartPolicy": "Never",
            "serviceAccountName": "e2e-test",
            "volumes": [{"name": "results", "emptyDir": {}}],
            "containers": [
                {
                    "name": TEST_POD_NAME,
                    "image": f"{os.getenv('BASE_REPO_URL')}/mongodb-community-tests:{os.getenv('VERSION_ID')}",
                    "imagePullPolicy": "Always",
                    "env": [
                        {
                            "name": "CLUSTER_WIDE",
                            "value": f"{args.cluster_wide}",
                        },
                        {
                            "name": "VERSION_ID",
                            "value": f"{os.getenv('VERSION_ID')}",
                        },
                        {
                            "name": "BASE_REPO_URL",
                            "value": f"{os.getenv('BASE_REPO_URL')}",
                        },
                        {
                            "name": "MDB_COMMUNITY_AGENT_IMAGE",
                            "value": f"{os.getenv('MDB_COMMUNITY_AGENT_IMAGE')}",
                        },
                        {
                            "name": "WATCH_NAMESPACE",
                            "value": namespace,
                        },
                        {
                            "name": "VERSION_UPGRADE_HOOK_IMAGE",
                            "value": f"{os.getenv('VERSION_UPGRADE_HOOK_IMAGE')}",
                        },
                        {
                            "name": "READINESS_PROBE_IMAGE",
                            "value": f"{os.getenv('READINESS_PROBE_IMAGE')}",
                        },
                        {
                            "name": "MDB_COMMUNITY_IMAGE",
                            "value": f"{os.getenv('MDB_COMMUNITY_IMAGE')}",
                        },
                        {
                            "name": "PERFORM_CLEANUP",
                            "value": f"{args.perform_cleanup}",
                        },
                    ],
                    "command": [
                        "sh",
                        "-c",
                        f"go test -v -timeout=45m -failfast ./mongodb-community-operator/test/e2e/{args.test} | tee -a /tmp/results/result.suite",
                    ],
                    "volumeMounts": [{"name": "results", "mountPath": "/tmp/results"}],
                },
                {
                    "name": "keepalive",
                    "image": "busybox",
                    "command": ["sh", "-c", "sleep inf"],
                    "volumeMounts": [{"name": "results", "mountPath": "/tmp/results"}],
                },
            ],
        },
    }
    if not k8s_conditions.wait(
        lambda: corev1.list_namespaced_pod(
            namespace,
            field_selector=f"metadata.name=={TEST_POD_NAME}",
        ),
        lambda pod_list: len(pod_list.items) == 0,
        timeout=30,
        sleep_time=0.5,
    ):
        raise Exception("Execution timed out while waiting for the existing pod to be deleted")

    if not k8s_conditions.call_eventually_succeeds(
        lambda: corev1.create_namespaced_pod(namespace, body=test_pod),
        sleep_time=10,
        timeout=60,
        exceptions_to_ignore=ApiException,
    ):
        raise Exception("Could not create test pod!")


def wait_for_pod_to_be_running(corev1: client.CoreV1Api, name: str, namespace: str) -> None:
    print("Waiting for pod to be running")
    if not k8s_conditions.wait(
        lambda: corev1.read_namespaced_pod(name, namespace),
        lambda pod: pod.status.phase == "Running",
        sleep_time=5,
        timeout=240,
        exceptions_to_ignore=ApiException,
    ):

        pod = corev1.read_namespaced_pod(name, namespace)
        raise Exception("Pod never got into Running state: {}".format(pod))
    print("Pod is running")


def _delete_test_environment(namespace) -> None:
    """
    _delete_test_environment ensures that the cluster role, cluster role binding and service account
    for the test pod are deleted.
    """
    rbacv1 = client.RbacAuthorizationV1Api()
    corev1 = client.CoreV1Api()

    k8s_conditions.ignore_if_doesnt_exist(lambda: rbacv1.delete_cluster_role(TEST_CLUSTER_ROLE_NAME))

    k8s_conditions.ignore_if_doesnt_exist(lambda: rbacv1.delete_cluster_role_binding(TEST_CLUSTER_ROLE_BINDING_NAME))

    k8s_conditions.ignore_if_doesnt_exist(
        lambda: corev1.delete_namespaced_service_account(TEST_SERVICE_ACCOUNT_NAME, namespace)
    )


def _delete_test_pod(namespace) -> None:
    """
    _delete_test_pod deletes the test pod.
    """
    corev1 = client.CoreV1Api()
    k8s_conditions.ignore_if_doesnt_exist(lambda: corev1.delete_namespaced_pod(TEST_POD_NAME, namespace))


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--test", help="Name of the test to run")
    parser.add_argument(
        "--perform-cleanup",
        help="Cleanup the context after executing the tests",
        action="store_true",
    )
    parser.add_argument(
        "--cluster-wide",
        help="Watch all namespaces",
        type=lambda x: x.lower() == "true",
    )
    parser.add_argument(
        "--distro",
        help="The distro of images that should be used",
        type=str,
        default="ubi",
    )
    parser.add_argument("--config_file", help="Path to the config file")
    return parser.parse_args()


def prepare_and_run_test(args: argparse.Namespace, namespace: str) -> None:
    _prepare_test_environment(namespace)
    create_test_pod(args, namespace)
    corev1 = client.CoreV1Api()

    wait_for_pod_to_be_running(
        corev1,
        TEST_POD_NAME,
        namespace,
    )

    print("stream all of the pod output as the pod is running")
    for line in corev1.read_namespaced_pod_log(
        TEST_POD_NAME, namespace, follow=True, _preload_content=False, container="e2e-test"
    ).stream():
        print(line.decode("utf-8").rstrip())


def main() -> int:
    args = parse_args()

    try:
        config.load_kube_config()
    except Exception:
        config.load_incluster_config()

    namespace = os.getenv("NAMESPACE")
    prepare_and_run_test(args, namespace)

    corev1 = client.CoreV1Api()
    if not k8s_conditions.wait(
        lambda: corev1.read_namespaced_pod(TEST_POD_NAME, namespace),
        lambda pod: any(
            container.state.terminated and container.state.terminated.exit_code == 0
            for container in pod.status.container_statuses
            if container.name == "e2e-test"
        ),
        sleep_time=5,
        timeout=60,
        exceptions_to_ignore=ApiException,
    ):
        return 1
    _delete_test_environment(namespace)
    return 0


if __name__ == "__main__":
    sys.exit(main())
