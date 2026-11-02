import json
import os
import re
import tempfile
from typing import Any, Dict, Iterator, List

import kubetester
import requests
from kubeobject import CustomObject
from kubernetes import client
from kubetester import run_periodically
from kubetester.kubetester import (
    assert_appdb_statefulset_architecture,
    assert_statefulset_architecture,
    get_default_architecture,
)
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.opsmanager import MongoDBOpsManager
from pymongo import WriteConcern
from pytest import fixture


def get_initial_olm_architecture() -> str:
    architecture = get_default_architecture()
    return "static" if architecture == "static-ga" else architecture


def set_olm_resource_architecture(resource: CustomObject, architecture: str):
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = architecture


def assert_olm_resource_architecture(resource: MongoDB | MongoDBOpsManager, architecture: str):
    resource.load()
    assert resource["metadata"]["annotations"]["mongodb.com/v1.architecture"] == architecture
    appdb = isinstance(resource, MongoDBOpsManager)
    if isinstance(resource, MongoDBOpsManager):
        statefulsets = [resource.read_appdb_statefulset()]
    elif resource["spec"]["type"] == "ShardedCluster":
        names = [resource.config_srv_statefulset_name(), resource.mongos_statefulset_name()]
        names += [resource.shard_statefulset_name(index) for index in range(resource["spec"]["shardCount"])]
        statefulsets = [client.AppsV1Api().read_namespaced_stateful_set(name, resource.namespace) for name in names]
    else:
        statefulsets = [resource.read_statefulset()]

    for statefulset in statefulsets:
        if appdb:
            assert_appdb_statefulset_architecture(statefulset, architecture)
        else:
            assert_statefulset_architecture(statefulset, architecture)
        labels = statefulset.spec.selector.match_labels
        assert labels
        pods = (
            client.CoreV1Api()
            .list_namespaced_pod(
                resource.namespace,
                label_selector=",".join(f"{key}={value}" for key, value in labels.items()),
            )
            .items
        )
        assert len(pods) == statefulset.spec.replicas
        expected = statefulset.spec.template.spec
        for pod in pods:
            assert pod.metadata.deletion_timestamp is None
            assert any(owner.uid == statefulset.metadata.uid for owner in pod.metadata.owner_references or [])
            assert any(
                condition.type == "Ready" and condition.status == "True" for condition in pod.status.conditions or []
            )
            assert (pod.spec.share_process_namespace is True) == (expected.share_process_namespace is True)
            expected_containers = {container.name: container for container in expected.containers}
            actual_containers = {container.name: container for container in pod.spec.containers}
            assert actual_containers.keys() == expected_containers.keys()
            for name, container in expected_containers.items():
                assert actual_containers[name].command == container.command
                assert actual_containers[name].image == container.image
            proxy_volumes = [volume for volume in pod.spec.volumes or [] if volume.name == "mongodb-proxy"]
            if architecture == "static-ga":
                assert len(proxy_volumes) == 1
                expected_proxy = next(volume for volume in expected.volumes if volume.name == "mongodb-proxy")
                assert proxy_volumes[0].image.reference == expected_proxy.image.reference
            else:
                assert not proxy_volumes


@fixture(scope="module")
def olm_upgrade_checks() -> Iterator[list[tuple[MongoTester, MongoDBBackgroundTester]]]:
    checks = []
    try:
        yield checks
    finally:
        for _, checker in checks:
            checker.stop()
        for tester, checker in checks:
            if checker.ident is not None:
                checker.join()
            tester.client.close()


def start_olm_upgrade_checks(
    ops_manager: MongoDBOpsManager,
    databases: list[MongoDB],
    ca_path: str,
    checks: list[tuple[MongoTester, MongoDBBackgroundTester]],
):
    assert not checks
    testers = [ops_manager.get_appdb_tester(authenticate=True)]
    testers += [database.tester(ca_path=ca_path) for database in databases]
    checks.extend((tester, MongoDBBackgroundTester(tester)) for tester in testers)
    for tester, checker in checks:
        collection = tester.client["olm_upgrade"]["sentinel"].with_options(write_concern=WriteConcern(w="majority"))
        collection.replace_one({"_id": "st1"}, {"_id": "st1", "value": "preserved"}, upsert=True)
        checker.start()


def assert_olm_upgrade_checks(checks: list[tuple[MongoTester, MongoDBBackgroundTester]]):
    assert len(checks) == 5
    for _, checker in checks:
        checker.stop()
    for tester, checker in checks:
        checker.join()
        checker.assert_healthiness()
        assert tester.client["olm_upgrade"]["sentinel"].find_one({"_id": "st1"}) == {
            "_id": "st1",
            "value": "preserved",
        }


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


def get_subscription_custom_object(name: str, namespace: str, spec: dict[str, Any]) -> CustomObject:
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
    # when running in pod, release.json will be available in current working dir (it's copied there in Dockerfile)
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


def get_release_json() -> dict[str, Any]:
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


def wait_for_operator_ready(namespace: str, name: str, expected_operator_version: str):
    def wait_for_operator_ready_fn():
        return check_operator_pod_ready_and_with_condition_version(namespace, name, expected_operator_version)

    run_periodically(
        wait_for_operator_ready_fn,
        timeout=120,
        msg=f"operator ready and with {expected_operator_version} version",
    )


def get_registry_env_vars_for_subscription(
    operator_installation_config: Dict[str, str],
    include_agent_registry: bool = False,
) -> List[Dict[str, str]]:
    """
    Returns registry env vars to add to OLM subscription config for patch builds.

    For upgrade tests in patch builds, we need to use ECR registries for OpsManager
    since new versions may not be released to quay.io yet.
    For staging/release builds, images are already published to quay.io, so we use quay.io
    to verify the actual public release path.

    Args:
        operator_installation_config: The operator installation config from ConfigMap.
        include_agent_registry: Whether to include agent registry override. Set to True
            for MCK-to-MCK upgrades (non-suffixed agent versions exist in ECR). Set to
            False for MEKO-to-MCK migrations (suffixed agent versions only on quay.io).

    This function returns the env vars in the format expected by OLM subscription config:
    [{"name": "ENV_VAR_NAME", "value": "value"}, ...]
    """
    build_scenario = operator_installation_config.get("buildScenario", "")

    if build_scenario != "patch":
        return []

    env_vars = []

    # Override OM registry for patch builds (unreleased versions not on quay.io)
    if "registry.opsManager" in operator_installation_config:
        ops_manager_name = operator_installation_config.get("opsManager.name", "mongodb-enterprise-ops-manager-ubi")
        registry = operator_installation_config["registry.opsManager"]
        env_vars.append({"name": "OPS_MANAGER_IMAGE_REPOSITORY", "value": f"{registry}/{ops_manager_name}"})

    # Override agent registry only for MCK-to-MCK upgrades (non-suffixed agent versions)
    # MEKO uses suffixed agent versions (e.g., 107.0.15.8741-1_1.33.0) that only exist on quay.io
    if include_agent_registry and "registry.agent" in operator_installation_config:
        registry = operator_installation_config["registry.agent"]
        env_vars.append({"name": "MDB_AGENT_IMAGE_REPOSITORY", "value": f"{registry}/mongodb-agent"})

    return env_vars
