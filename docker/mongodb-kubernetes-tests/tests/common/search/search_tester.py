import os
from dataclasses import dataclass

import kubetester
from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import MongoTester
from pymongo.operations import SearchIndexModel
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod

logger = test_logger.get_test_logger(__name__)


class SearchTester(MongoTester):
    def __init__(
        self,
        connection_string: str,
        use_ssl: bool = False,
        ca_path: str | None = None,
    ):
        super().__init__(connection_string, use_ssl, ca_path)

    def mongorestore_from_url(self, archive_url: str, ns_include: str, tools_pod: mongodb_tools_pod.ToolsPod):
        logger.debug(f"running mongorestore from {archive_url} in pod {tools_pod.pod_name}")

        archive_file = f"/tmp/mongodb_archive_{os.urandom(4).hex()}.archive"

        logger.debug(f"Downloading archive to {archive_file}")
        download_cmd = ["curl", "-L", "-o", archive_file, archive_url]
        tools_pod.run_command(cmd=download_cmd)

        logger.debug("Running mongorestore")
        mongorestore_cmd = [
            "mongorestore",
            f"--archive={archive_file}",
            "--verbose=1",
            "--drop",
            f"--nsInclude={ns_include}",
            f"--uri={self.cnx_string}",
        ]

        if self.default_opts.get("tls", False):
            mongorestore_cmd.append("--ssl")
        if ca_path := self.default_opts.get("tlsCAFile"):
            tools_pod.copy_file_to_pod(ca_path, "/tmp/ca.crt")
            mongorestore_cmd.append(f"--sslCAFile=/tmp/ca.crt")

        result = tools_pod.run_command(cmd=mongorestore_cmd)
        logger.debug(f"mongorestore completed: {result}")

        logger.debug("Cleaning up archive file")
        cleanup_cmd = ["rm", "-f", archive_file]
        tools_pod.run_command(cmd=cleanup_cmd)

        return result

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
