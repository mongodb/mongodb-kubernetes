import os
import platform
import shutil
import sys
import tarfile
import tempfile

import kubernetes
import kubetester
import pymongo
import requests
from kubernetes import client as k8s_client
from kubetester import create_or_update_secret, try_load
from kubetester.helm import process_run_and_check
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.operator import Operator
from pymongo.operations import SearchIndexModel
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import LEGACY_CENTRAL_CLUSTER_NAME

logger = test_logger.get_test_logger(__name__)

USER_PASSWORD = "Passw0rd."
USER_NAME = "my-user"
MDBC_RESOURCE_NAME = "mdbc-rs"


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    resource["spec"]["version"] = "8.0.5"

    if try_load(resource):
        return resource

    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )

    # resource["spec"]["source"]["mongodbResourceRef"]["name"] = MDBC_RESOURCE_NAME

    if try_load(resource):
        return resource

    return resource


@mark.e2e_search_community_basic
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_search_community_basic
def test_install_mdbdebug(namespace: str):
    """Create a mdbdebug deployment to help with debugging."""
    logger.info("Creating mdbdebug deployment")

    # Create the deployment object
    deployment = k8s_client.V1Deployment(
        api_version="apps/v1",
        kind="Deployment",
        metadata=k8s_client.V1ObjectMeta(name="mdbdebug", namespace=namespace),
        spec=k8s_client.V1DeploymentSpec(
            replicas=1,
            selector=k8s_client.V1LabelSelector(match_labels={"app": "mdbdebug"}),
            template=k8s_client.V1PodTemplateSpec(
                metadata=k8s_client.V1ObjectMeta(labels={"app": "mdbdebug"}),
                spec=k8s_client.V1PodSpec(
                    service_account_name="operator-tests-service-account",
                    containers=[
                        k8s_client.V1Container(
                            name="mdbdebug",
                            image="quay.io/lsierant/mdbdebug",
                            args=[
                                "--watch",
                                "--deployPods",
                                "--operator-namespace",
                                namespace,
                                "--namespace",
                                namespace,
                                "--context",
                                LEGACY_CENTRAL_CLUSTER_NAME,
                            ],
                        )
                    ],
                ),
            ),
        ),
    )

    # Create the deployment
    api_instance = k8s_client.AppsV1Api()
    try:
        api_instance.create_namespaced_deployment(namespace=namespace, body=deployment)
    except kubernetes.client.ApiException as e:
        if e.status == 409:
            api_instance.replace_namespaced_deployment("mdbdebug", namespace, deployment)
        else:
            raise Exception(f"failed to deployment: {e}")

    logger.info("Waiting for mdbdebug deployment to be ready")
    apps_v1 = k8s_client.AppsV1Api()

    def is_deployment_ready():
        deployment = apps_v1.read_namespaced_deployment(name="mdbdebug", namespace=namespace)
        if deployment.status.ready_replicas is not None and deployment.status.ready_replicas > 0:
            return True
        return False

    kubetester.run_periodically(
        fn=is_deployment_ready, timeout=60, sleep_time=2, msg="Waiting for mdbdebug deployment to be ready"
    )

    logger.info("mdbdebug deployment is ready")


@mark.e2e_search_community_basic
def test_install_secret(namespace: str):
    create_or_update_secret(namespace=namespace, name="my-user-password", data={"password": USER_PASSWORD})


@mark.e2e_search_community_basic
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_community_basic
def test_create_search_resource(mdbs: MongoDBSearch, mdbc: MongoDBCommunity):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)
    mdbc.assert_reaches_phase(Phase.Running, timeout=800)


def import_sample_database(sample_url: str, connection_string: str, mongodb_tools_dir: str):
    with tempfile.NamedTemporaryFile(delete=False) as sample_file:
        resp = requests.get(sample_url)
        print(f"Downloaded sample file from {sample_url} to {sample_file.name}")
        size = sample_file.write(resp.content)
        print(f"Downloaded sample file from {sample_url} to {sample_file.name} (size: {size})")
        mongorestore_path = os.path.join(mongodb_tools_dir, "mongorestore")
        mongorestore_cmd = f"{mongorestore_path} --archive={sample_file.name} --verbose=1 --drop --nsInclude sample_mflix.* --uri={connection_string}"
        print(f"Executing: {mongorestore_cmd}")
        process_run_and_check(mongorestore_cmd.split())


@mark.e2e_search_community_basic
def test_import_sample_database(mdbc: MongoDBCommunity, mongodb_tools_dir: str):
    url = "https://atlas-education.s3.amazonaws.com/sample_mflix.archive"
    import_sample_database(url, get_connection_string(mdbc), mongodb_tools_dir)


@mark.e2e_search_community_basic
def test_create_search_index(mdbc: MongoDBCommunity):
    client = pymongo.MongoClient(get_connection_string(mdbc))
    try:
        # sample taken from: https://www.mongodb.com/docs/atlas/atlas-search/tutorial/create-index/#copy-and-paste-the-following-code-into-the-create_index.py-file.
        # set namespace
        database = client["sample_mflix"]
        collection = database["movies"]
        # define your Atlas Search index
        search_index_model = SearchIndexModel(
            definition={
                "mappings": {"dynamic": True},
            },
            name="default",
        )
        # create the index
        result = collection.create_search_index(model=search_index_model)
        print(result)
    finally:
        client.close()


@mark.e2e_search_community_basic
def test_wait_for_search_indexes(mdbc: MongoDBCommunity):
    client = pymongo.MongoClient(get_connection_string(mdbc))
    kubetester.run_periodically(
        fn=lambda: search_indexes_ready(client, "sample_mflix", "movies"),
        timeout=60,
        sleep_time=1,
        msg="Waiting for search indexes to be ready",
    )


def search_indexes_ready(client: pymongo.MongoClient, database_name: str, collection_name: str):
    search_indexes = get_search_indexes(client, database_name, collection_name)
    for idx in search_indexes:
        if idx.get("status") != "READY":
            logger.debug(f"search index {idx} is not ready")
            return False
    return True


@mark.e2e_search_community_basic
def test_search_query(mdbc: MongoDBCommunity):
    client = pymongo.MongoClient(get_connection_string(mdbc))
    try:
        # sample taken from: https://www.mongodb.com/docs/atlas/atlas-search/tutorial/run-query/#run-a-complex-query-on-the-movies-collection-7
        result = client["sample_mflix"]["movies"].aggregate(
            [
                {
                    "$search": {
                        "compound": {
                            "must": [
                                {"text": {"query": ["Hawaii", "Alaska"], "path": "plot"}},
                                {"regex": {"query": "([0-9]{4})", "path": "plot", "allowAnalyzedField": True}},
                            ],
                            "mustNot": [
                                {"text": {"query": ["Comedy", "Romance"], "path": "genres"}},
                                {"text": {"query": ["Beach", "Snow"], "path": "title"}},
                            ],
                        }
                    }
                },
                {"$project": {"title": 1, "plot": 1, "genres": 1, "_id": 0}},
            ]
        )

        print(f"Search query results:")
        count = 0
        for r in result:
            print(r)
            count += 1

        assert count == 4
    finally:
        client.close()


def get_connection_string(mdbc: MongoDBCommunity) -> str:
    return f"mongodb://{USER_NAME}:{USER_PASSWORD}@{mdbc.name}-0.{mdbc.name}-svc.{mdbc.namespace}.svc.cluster.local:27017/?replicaSet={mdbc.name}"


def get_search_indexes(client, database_name, collection_name):
    indexes = list(client[database_name][collection_name].list_search_indexes())
    logger.debug(f"Found {len(indexes)} search indexes in {database_name}.{collection_name}")
    for idx in indexes:
        logger.debug(f"Search index: {idx.get('name')} - Status: {idx.get('status')}")
    return indexes
