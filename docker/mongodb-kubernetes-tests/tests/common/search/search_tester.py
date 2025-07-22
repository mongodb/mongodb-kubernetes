import os
import tempfile

import kubetester
import requests
from kubetester.helm import process_run_and_check
from kubetester.mongotester import MongoTester
from pymongo.operations import SearchIndexModel
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


class SearchTester(MongoTester):
    def __init__(
        self,
        connection_string: str,
        use_ssl: bool = False,
        ca_path: str | None = None,
    ):
        super().__init__(connection_string, use_ssl, ca_path)

    def mongorestore_from_url(self, archive_url: str, ns_include: str, mongodb_tools_dir: str = ""):
        logger.debug(f"running mongorestore from {archive_url}")
        with tempfile.NamedTemporaryFile(delete=False) as sample_file:
            resp = requests.get(archive_url)
            size = sample_file.write(resp.content)
            logger.debug(f"Downloaded sample file from {archive_url} to {sample_file.name} (size: {size})")
            mongorestore_path = os.path.join(mongodb_tools_dir, "mongorestore")
            mongorestore_cmd = f"{mongorestore_path} --archive={sample_file.name} --verbose=1 --drop --nsInclude {ns_include} --uri={self.cnx_string}"
            if self.default_opts.get("tls", False):
                mongorestore_cmd += " --ssl"
            if ca_path := self.default_opts.get("tlsCAFile"):
                mongorestore_cmd += " --sslCAFile=" + ca_path
            process_run_and_check(mongorestore_cmd.split())

    def create_search_index(self, database_name: str, collection_name: str):
        database = self.client[database_name]
        collection = database[collection_name]
        search_index_model = SearchIndexModel(
            definition={
                "mappings": {"dynamic": True},
            },
        )
        result = collection.create_search_index(model=search_index_model)
        logger.debug(f"create_search_index result: {result}")

    def wait_for_search_indexes_ready(self, database_name: str, collection_name: str, timeout=60):
        kubetester.run_periodically(
            fn=lambda: self.search_indexes_ready(database_name, collection_name),
            timeout=timeout,
            sleep_time=1,
            msg="Waiting for search indexes to be ready",
        )

    def search_indexes_ready(self, database_name: str, collection_name: str):
        search_indexes = self.get_search_indexes(database_name, collection_name)
        if len(search_indexes) == 0:
            logger.error(f"There are no search indexes available in {database_name}.{collection_name}")
            return False

        for idx in search_indexes:
            if idx.get("status") != "READY":
                logger.debug(f"{database_name}/{collection_name}: search index {idx} is not ready")
                return False
        return True

    def get_search_indexes(self, database_name, collection_name):
        indexes = list(self.client[database_name][collection_name].list_search_indexes())
        logger.debug(f"Found {len(indexes)} search indexes in {database_name}.{collection_name}")
        for idx in indexes:
            logger.debug(f"Search index: {idx.get('name')} - Status: {idx.get('status')}")
        return indexes
