import tempfile

import pymongo
import requests
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
def test_install_operator(community_operator: Operator):
    community_operator.assert_is_running()


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
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


def import_sample_database(sample_url: str, connection_string: str):
    with tempfile.NamedTemporaryFile(delete=False) as sample_file:
        resp = requests.get(sample_url)
        print(f"Downloaded sample file from {sample_url} to {sample_file.name}")
        size = sample_file.write(resp.content)
        print(f"Downloaded sample file from {sample_url} to {sample_file.name} (size: {size})")
        mongorestore_cmd = f"mongorestore --archive={sample_file.name} --verbose=1 --drop --nsInclude sample_mflix.* --uri={connection_string}"
        print(f"Executing: {mongorestore_cmd}")
        process_run_and_check(mongorestore_cmd.split())


@mark.e2e_search_community_basic
def test_import_sample_database(mdbc: MongoDBCommunity):
    url = "https://atlas-education.s3.amazonaws.com/sample_mflix.archive"

    import_sample_database(url, get_connection_string(mdbc))


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
def test_test_search_query(mdbc: MongoDBCommunity):
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
