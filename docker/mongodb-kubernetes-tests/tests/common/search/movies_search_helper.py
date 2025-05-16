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

    def assert_search_query(self):
        # sample taken from: https://www.mongodb.com/docs/atlas/atlas-search/tutorial/run-query/#run-a-complex-query-on-the-movies-collection-7
        result = self.search_tester.client[self.db_name][self.col_name].aggregate(
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

        logger.debug(f"{self.db_name}/{self.col_name}: search query results:")
        count = 0
        for r in result:
            logger.debug(r)
            count += 1

        assert count == 4
