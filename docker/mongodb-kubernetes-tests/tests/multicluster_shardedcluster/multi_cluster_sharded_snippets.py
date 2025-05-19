import os
from typing import List

from kubetester import create_or_update_configmap, read_configmap, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import mark
from tests import test_logger

SNIPPETS_DIR = "public/samples/sharded_multicluster/"
SNIPPETS_FILES = [
    "pod_template_shards_0.yaml",
    "pod_template_shards_1.yaml",
    "pod_template_config_servers.yaml",
    "shardSpecificPodSpec_migration.yaml",
    "example-sharded-cluster-deployment.yaml",
]

logger = test_logger.get_test_logger(__name__)


def load_resource(namespace: str, file_path: str, resource_name: str = None) -> MongoDB:
    resource = MongoDB.from_yaml(file_path, namespace=namespace, name=resource_name)
    return resource


def get_project_directory() -> str:
    project_dir = os.environ.get("PROJECT_DIR")
    logger.debug(f"PROJECT_DIR: {project_dir}")
    return project_dir


# To be able to access the snippets file from here, we added "COPY public /mongodb-kubernetes/public" when building
# the mongodb-test container
# Then we set the env variable PROJECT_DIR to /mongodb-kubernetes
# The test will also work locally if the variable is set correctly
def get_sharded_resources(namespace: str) -> List[MongoDB]:
    resources = []
    project_directory = get_project_directory()
    files_dir = os.path.join(project_directory, SNIPPETS_DIR)
    for file_name in SNIPPETS_FILES:
        file_path = os.path.join(files_dir, file_name)
        logger.debug(f"Loading snippet file: {file_path}")
        logger.debug(f"File found: {os.path.isfile(file_path)}")
        # We set the resource name as the file name, but replace _ with - and lowercase,
        # to respect kubernetes naming constraints
        sc = load_resource(namespace, file_path)
        sc["spec"]["opsManager"]["configMapRef"]["name"] = f"{file_to_resource_name(file_name)}-project-map"
        resources.append(sc)
    return resources


def file_to_resource_name(file_name: str) -> str:
    return file_name.removesuffix(".yaml").replace("_", "-").lower()


@mark.e2e_multi_cluster_sharded_snippets
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_snippets
def test_create_projects_configmaps(namespace: str):
    for file_name in SNIPPETS_FILES:
        base_cm = read_configmap(namespace=namespace, name="my-project")
        # Validate required keys
        required_keys = ["baseUrl", "orgId", "projectName"]
        for key in required_keys:
            if key not in base_cm:
                raise KeyError(f"The OM/CM project configmap is missing the key: {key}")

        create_or_update_configmap(
            namespace=namespace,
            name=f"{file_to_resource_name(file_name)}-project-map",
            data={
                "baseUrl": base_cm["baseUrl"],
                "orgId": base_cm["orgId"],
                # In EVG, we generate a unique ID for the project name in the 'my-project' configmap when we set up a
                # test. To avoid project name collisions in between two concurrently running tasks in CloudQA,
                # we concatenate it to the name of the mdb resource
                "projectName": f"{base_cm['projectName']}-{file_to_resource_name(file_name)}",
            },
        )


@mark.e2e_multi_cluster_sharded_snippets
def test_create(namespace: str, custom_mdb_version: str, issuer_ca_configmap: str):
    for sc in get_sharded_resources(namespace):
        sc.set_version(ensure_ent_version(custom_mdb_version))
        sc.update()


# All resources will be reconciled in parallel, we wait for all of them to reach Running to succeed
# Catching exceptions enables to display all failing resources instead of just the first, and makes debugging easier
@mark.e2e_multi_cluster_sharded_snippets
def test_running(namespace: str):
    succeeded_resources = []
    failed_resources = []
    first_iter = True

    for sc in get_sharded_resources(namespace):
        try:
            logger.debug(f"Waiting for {sc.name} to reach Running phase")
            # Once the first resource reached Running, it shouldn't take more than ~300s for the others to do so
            sc.assert_reaches_phase(Phase.Running, timeout=900 if first_iter else 300)
            succeeded_resources.append(sc.name)
            first_iter = False
            logger.info(f"{sc.name} reached Running phase")
        except Exception as e:
            logger.error(f"Error while waiting for {sc.name} to reach Running phase: {e}")
            failed_resources.append(sc.name)

    if succeeded_resources:
        logger.info(f"Resources that reached Running phase: {', '.join(succeeded_resources)}")

    # Ultimately fail the test if any resource failed to reconcile
    if failed_resources:
        raise AssertionError(f"Some resources failed to reach Running phase: {', '.join(failed_resources)}")
    else:
        logger.info(f"All resources reached Running phase")
