from typing import TYPE_CHECKING

import pymongo.errors
from kubetester import kubetester
from tests import test_logger
from tests.common.search.search_tester import SearchTester

if TYPE_CHECKING:
    from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod

logger = test_logger.get_test_logger(__name__)


class SampleMoviesSearchHelper:
    search_tester: SearchTester
    db_name: str
    col_name: str
    archive_url: str
    tools_pod: "ToolsPod"

    def __init__(self, search_tester: SearchTester, tools_pod: "ToolsPod"):
        self.search_tester = search_tester
        self.tools_pod = tools_pod
        self.db_name = "sample_mflix"
        self.col_name = "movies"

    def restore_sample_database(self):
        self.search_tester.mongorestore_from_url(
            "https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
            f"{self.db_name}.*",
            self.tools_pod,
        )

    def create_search_index(self):
        self.search_tester.create_search_index(self.db_name, self.col_name)

    def create_auto_embedding_vector_search_index(self):
        self.search_tester.create_auto_embedding_vector_search_index(self.db_name, self.col_name)

    def wait_for_search_indexes(self):
        self.search_tester.wait_for_search_indexes_ready(self.db_name, self.col_name)

    def assert_search_query(self, retry_timeout: int = 1):
        def wait_for_search_results():
            # sample taken from: https://www.mongodb.com/docs/atlas/atlas-search/tutorial/run-query/#run-a-complex-query-on-the-movies-collection-7
            count = 0
            status_msg = ""
            try:
                result = self.execute_example_search_query()
                status_msg = f"{self.db_name}/{self.col_name}: search query results:\n"
                for r in result:
                    status_msg += f"{r}\n"
                    count += 1
                status_msg += f"Count: {count}"
                logger.debug(status_msg)
            except pymongo.errors.PyMongoError as e:
                logger.debug(f"error: {e}")

            return count == 4, status_msg

        kubetester.run_periodically(
            fn=wait_for_search_results,
            timeout=retry_timeout,
            sleep_time=1,
            msg="Search query to return correct data",
        )

    def execute_example_search_query(self):
        return self.search_tester.client[self.db_name][self.col_name].aggregate(
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

    def execute_auto_embedding_vector_search_query(self, query: str = "spy thriller", limit: int = 10):
        return self.search_tester.client[self.db_name][self.col_name].aggregate(
            [
                {
                    "$vectorSearch": {
                        "index": "vector_auto_embed_index",
                        "path": "plot",
                        "query": query,
                        "numCandidates": 150,
                        "limit": limit,
                    }
                },
                {
                    "$project": {
                        "_id": 0,
                        "plot": 1,
                        "title": 1,
                        "score": {"$meta": "vectorSearchScore"},
                    }
                },
            ]
        )

    def assert_auto_emb_vector_search_query(self, retry_timeout: int = 1):
        def wait_for_auto_emb_search_results():
            exp_document_count = 10
            count = 0
            status_msg = ""
            try:
                result = self.execute_auto_embedding_vector_search_query(limit=exp_document_count)
                status_msg = f"{self.db_name}/{self.col_name}: auto-embedding vector search query results:\n"
                for r in result:
                    status_msg += f"{r}\n"
                    count += 1
                status_msg += f"Count: {count}"
                logger.debug(status_msg)
            except pymongo.errors.PyMongoError as e:
                logger.debug(f"error: {e}")

            return count == exp_document_count, status_msg

        kubetester.run_periodically(
            fn=wait_for_auto_emb_search_results,
            timeout=retry_timeout,
            sleep_time=1,
            msg="Auto-embedding vector search query to return correct data",
        )
