import logging

import pymongo.errors

from kubetester import kubetester
from tests import test_logger
from tests.common.search.search_tester import SearchTester

logger = test_logger.get_test_logger(__name__)


class SampleMoviesSearchHelper:
    search_tester: SearchTester
    db_name: str
    col_name: str
    archive_url: str

    def __init__(self, search_tester: SearchTester):
        self.search_tester = search_tester
        self.db_name = "sample_mflix"
        self.col_name = "movies"

    def restore_sample_database(self):
        self.search_tester.mongorestore_from_url(
            "https://atlas-education.s3.amazonaws.com/sample_mflix.archive", f"{self.db_name}.*"
        )

    def create_search_index(self):
        self.search_tester.create_search_index(self.db_name, self.col_name)

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
