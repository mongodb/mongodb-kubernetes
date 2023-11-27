import os
import tempfile
import time
from typing import Callable

import yaml
from kubeobject import CustomObject
from kubernetes import client

import kubetester
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
        "displayName": "MongoDB Enterprise Operator upgrade test",
        "publisher": "MongoDB",
        "updateStrategy": {"registryPoll": {"interval": "5m"}},
    }

    return resource


def get_package_manifest_resource(
    namespace: str, manifest_name: str = "mongodb-enterprise"
) -> CustomObject:
    return CustomObject(
        manifest_name,
        namespace,
        "PackageManifest",
        "packagemanifests",
        "packages.operators.coreos.com",
        "v1",
    )


def get_subscription_custom_object(
    name: str, namespace: str, spec: dict[str, str]
) -> CustomObject:
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
        raise Exception(
            "Cannot get base registry url, specify it in REGISTRY env variable."
        )

    return registry


def get_catalog_image(version: str):
    return f"{get_registry()}/mongodb-enterprise-operator-certified-catalog:{version}"


def list_operator_pods(namespace: str, name: str) -> list[client.V1Pod]:
    return client.CoreV1Api().list_namespaced_pod(
        namespace, label_selector=f"app.kubernetes.io/name={name}"
    )


def check_operator_pod_ready_and_with_condition_version(
    namespace: str, name: str, expected_condition_version
) -> tuple[str, str]:
    pod = kubetester.is_pod_ready(
        namespace=namespace, label_selector=f"app.kubernetes.io/name={name}"
    )
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
    operator_condition_env = [
        e for e in operator_container.env if e.name == "OPERATOR_CONDITION_NAME"
    ]
    if len(operator_condition_env) == 0:
        return None
    return operator_condition_env[0].value


def get_current_operator_version(namespace: str):
    package_manifest = get_package_manifest_resource(namespace)
    if not kubetester.try_load(package_manifest):
        print(
            "The PackageManifest doesn't exist. Falling back to using Helm Chart for obtaining version"
        )
        with open("helm_chart/values.yaml", "r") as f:
            values = yaml.safe_load(f)
            return values.get("operator", {}).get("version", None)
    for channel in package_manifest["status"]["channels"]:
        if channel["name"] == "stable":
            return channel["currentCSVDesc"]["version"]
            # [0] is the fast channel and [1] is the stable one.
    raise Exception(
        f"Could not find the stable channel in the PackageManifest. The full object: {package_manifest}"
    )


def increment_patch_version(version: str):
    major, minor, patch = version.split(".")
    return ".".join([major, minor, str(int(patch) + 1)])


def wait_for_operator_ready(namespace: str, expected_operator_version: str):
    def wait_for_operator_ready_fn():
        return check_operator_pod_ready_and_with_condition_version(
            namespace, "mongodb-enterprise-operator", expected_operator_version
        )

    run_periodically(
        wait_for_operator_ready_fn,
        timeout=120,
        msg=f"operator ready and with {expected_operator_version} version",
    )
