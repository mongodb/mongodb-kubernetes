import json
import os
import re
import tempfile
import time
from typing import Callable

import kubetester
import pytest
import requests
import yaml
from kubeobject import CustomObject
from kubernetes import client
from kubetester import run_periodically


def custom_object_from_yaml(yaml_string: str) -> CustomObject:
    tmpfile_name = tempfile.mkstemp()[1]
    try:
        with open(tmpfile_name, "w") as tmpfile:
            tmpfile.write(yaml_string)
        return CustomObject.from_yaml(tmpfile_name)
    finally:
        os.remove(tmpfile_name)


def get_operator_group_resource(namespace: str, target_namespace: str) -> CustomObject:
    resource = CustomObject(
        "mongodb-group",
        namespace,
        "OperatorGroup",
        "operatorgroups",
        "operators.coreos.com",
        "v1",
    )
    resource["spec"] = {"targetNamespaces": [target_namespace]}

    return resource


def get_catalog_source_custom_object(namespace: str, name: str):
    return CustomObject(
        name,
        namespace,
        "CatalogSource",
        "catalogsources",
        "operators.coreos.com",
        "v1alpha1",
    )


def get_catalog_source_resource(namespace: str, image: str) -> CustomObject:
    resource = get_catalog_source_custom_object(namespace, "mongodb-operator-catalog")
    resource["spec"] = {
        "image": image,
        "sourceType": "grpc",
        "displayName": "MongoDB Kubernetes Operator upgrade test",
        "publisher": "MongoDB",
        "updateStrategy": {"registryPoll": {"interval": "5m"}},
    }

    return resource


def get_subscription_custom_object(name: str, namespace: str, spec: dict[str, str]) -> CustomObject:
    resource = CustomObject(
        name,
        namespace,
        "Subscription",
        "subscriptions",
        "operators.coreos.com",
        "v1alpha1",
    )
    resource["spec"] = spec
    return resource


def get_registry():
    registry = os.getenv("REGISTRY")
    if registry is None:
        raise Exception("Cannot get base registry url, specify it in REGISTRY env variable.")

    return registry


def get_catalog_image(version: str):
    return f"{get_registry()}/mongodb-kubernetes-test-catalog:{version}"


def list_operator_pods(namespace: str, name: str) -> list[client.V1Pod]:
    return client.CoreV1Api().list_namespaced_pod(namespace, label_selector=f"app.kubernetes.io/name={name}")


def check_operator_pod_ready_and_with_condition_version(
    namespace: str, name: str, expected_condition_version
) -> tuple[bool, str]:
    pod = kubetester.is_pod_ready(namespace=namespace, label_selector=f"app.kubernetes.io/name={name}")
    if pod is None:
        return False, f"pod {namespace}/{name} is not ready yet"

    condition_env_var = get_pod_condition_env_var(pod)
    if condition_env_var != expected_condition_version:
        return (
            False,
            f"incorrect condition env var: expected {expected_condition_version}, got {condition_env_var}",
        )

    return True, ""


def get_pod_condition_env_var(pod):
    operator_container = pod.spec.containers[0]
    operator_condition_env = [e for e in operator_container.env if e.name == "OPERATOR_CONDITION_NAME"]
    if len(operator_condition_env) == 0:
        return None
    return operator_condition_env[0].value


def get_release_json_path() -> str:
    # when running in pod, release.json will be available in /release.json (it's copied there in Dockerfile)
    if os.path.exists("release.json"):
        return "release.json"
    else:
        # when running locally, we try to read it from the project's dir
        release_json_path = os.path.join(os.environ["PROJECT_DIR"], "release.json")
        print(f"release.json not found in current path, checking {release_json_path}")
        if os.path.exists(release_json_path):
            return release_json_path
        else:
            raise Exception(
                "release.json file not found, ensure it's copied into test pod or $PROJECT_DIR ({os.environ['PROJECT_DIR']}) is set to mongodb-kubernetes dir"
            )


def get_release_json() -> dict[str, any]:
    with open(get_release_json_path()) as f:
        return json.load(f)


def get_current_operator_version() -> str:
    return get_release_json()["mongodbOperator"]


def get_latest_released_operator_version(package_name: str) -> str:
    released_operators_url = (
        f"https://api.github.com/repos/redhat-openshift-ecosystem/certified-operators/contents/operators/{package_name}"
    )
    response = requests.get(released_operators_url, headers={"Accept": "application/vnd.github.v3+json"})

    if response.status_code != 200:
        raise Exception(
            f"Error getting contents of released operators dir {released_operators_url} in certified operators repo: {response.status_code}"
        )

    data = response.json()
    version_pattern = re.compile(r"(\d+\.\d+\.\d+)")
    versioned_directories = [
        item["name"] for item in data if item["type"] == "dir" and version_pattern.match(item["name"])
    ]
    if not versioned_directories:
        raise Exception(
            f"Error getting contents of released operators dir {released_operators_url} in certified operators repo: there are no versions"
        )

    print(f"Received list of versions from {released_operators_url}: {versioned_directories}")

    # GitHub is returning sorted directories, so the last one is the latest released operator
    return versioned_directories[-1]


def increment_patch_version(version: str):
    major, minor, patch = version.split(".")
    return ".".join([major, minor, str(int(patch) + 1)])


def wait_for_operator_ready(namespace: str, name: str, expected_operator_version: str):
    def wait_for_operator_ready_fn():
        return check_operator_pod_ready_and_with_condition_version(namespace, name, expected_operator_version)

    run_periodically(
        wait_for_operator_ready_fn,
        timeout=120,
        msg=f"operator ready and with {expected_operator_version} version",
    )
