import os
from typing import Optional

import pymongo.errors
import requests
from kubetester import kubetester
from pymongo.operations import SearchIndexModel
from tests import test_logger
from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod
from tests.common.search.search_tester import SearchTester

logger = test_logger.get_test_logger(__name__)

VOYAGE_EMBEDDING_ENDPOINT = "https://ai.mongodb.com/v1/embeddings"
VOYAGE_MODEL = "voyage-3-large"
VOYAGE_DIMENSIONS = 2048
EMBEDDING_QUERY_KEY_ENV_VAR = "AI_MONGODB_EMBEDDING_QUERY_KEY"


class SampleMoviesSearchHelper:
    search_tester: SearchTester
    db_name: str
    col_name: str
    archive_url: str
    tools_pod: Optional[ToolsPod]

    def __init__(self, search_tester: SearchTester, tools_pod: Optional[ToolsPod] = None):
        self.search_tester = search_tester
        self.tools_pod = tools_pod
        self.db_name = "sample_mflix"
        self.col_name = "movies"

    def restore_sample_database(self):
        if self.tools_pod is None:
            raise ValueError(
                "tools_pod is required for restore_sample_database, pass it to the constructor while creating SampleMoviesSearchHelper instance"
            )
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

    def text_search_movies(self, query: str, index: str = "default", path: str = "title", limit: int = 10):
        return list(
            self.search_tester.client[self.db_name][self.col_name].aggregate(
                [
                    {"$search": {"index": index, "text": {"query": query, "path": path}}},
                    {"$limit": limit},
                    {"$project": {"_id": 0, "title": 1, "score": {"$meta": "searchScore"}}},
                ]
            )
        )

    def wildcard_search_movies(self, wildcard: str = "*", index: str = "default"):
        return list(
            self.search_tester.client[self.db_name][self.col_name].aggregate(
                [
                    {
                        "$search": {
                            "index": index,
                            "wildcard": {"query": wildcard, "path": "title", "allowAnalyzedField": True},
                        }
                    },
                    {"$project": {"_id": 0, "title": 1}},
                ]
            )
        )

    # get_shard_document_counts returns the documents counts in each shard for a specific collection.
    # It is supposed to be called after sharding a collection and making sure that
    # the chunks have been distributed properly to the shards.
    def get_shard_document_counts(self) -> dict[str, int]:
        """Get per-shard document counts for the collection.

        Returns:
            Dict mapping shard name to document count stored in that shard.
        """
        db = self.search_tester.client[self.db_name]
        stats = db.command("collStats", self.col_name)
        shard_counts = {}
        for shard_name, shard_stats in stats["shards"].items():
            shard_counts[shard_name] = shard_stats["count"]
            logger.info(f"Shard {shard_name}: {shard_stats['count']} documents")
        return shard_counts


class EmbeddedMoviesSearchHelper:

    def __init__(self, search_tester: SearchTester):
        self.search_tester = search_tester
        self.db_name = "sample_mflix"
        self.col_name = "embedded_movies"

    def create_vector_search_index(self, index_name: str = "vector_index"):
        """Create a vector search index on the plot_embedding_voyage_3_large field."""
        db = self.search_tester.client[self.db_name]
        collection = db[self.col_name]
        search_index_model = SearchIndexModel(
            definition={
                "fields": [
                    {
                        "type": "vector",
                        "path": "plot_embedding_voyage_3_large",
                        "numDimensions": VOYAGE_DIMENSIONS,
                        "similarity": "cosine",
                    }
                ]
            },
            type="vectorSearch",
            name=index_name,
        )
        result = collection.create_search_index(model=search_index_model)
        logger.debug(f"create_vector_search_index result: {result}")

    def wait_for_vector_search_index(self, timeout: int = 300):
        """Wait for vector search indexes to be ready on embedded_movies."""
        self.search_tester.wait_for_search_indexes_ready(self.db_name, self.col_name, timeout=timeout)

    def generate_query_vector(self, query_text: str) -> list:
        """Generate an embedding vector for the given text by calling the Voyage embedding API.

        Calls the Voyage API via the MongoDB proxy endpoint to generate a vector
        for the given query text. This vector can then be passed to vector_search().

        Args:
            query_text: The text to generate an embedding for (e.g., "war movies").

        Returns:
            A list of floats representing the embedding vector (2048 dimensions).

        Raises:
            ValueError: If the API key env var is not set.
            requests.HTTPError: If the API call fails.
        """
        api_key = os.getenv(EMBEDDING_QUERY_KEY_ENV_VAR)
        if not api_key:
            raise ValueError(f"Missing required environment variable: {EMBEDDING_QUERY_KEY_ENV_VAR}")

        response = requests.post(
            VOYAGE_EMBEDDING_ENDPOINT,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
            json={
                "model": VOYAGE_MODEL,
                "input": [query_text],
                "output_dimension": VOYAGE_DIMENSIONS,
            },
        )
        response.raise_for_status()

        data = response.json()
        vector = data["data"][0]["embedding"]
        logger.debug(f"Generated query vector for '{query_text}': {len(vector)} dimensions")
        return vector

    def count_documents_with_embeddings(self) -> int:
        """Count documents that have the plot_embedding_voyage_3_large field."""
        db = self.search_tester.client[self.db_name]
        collection = db[self.col_name]
        count = collection.count_documents({"plot_embedding_voyage_3_large": {"$exists": True}})
        logger.debug(f"Documents with plot_embedding_voyage_3_large: {count}")
        return count

    def vector_search(self, query_vector: list, limit: int, index_name: str = "vector_index") -> list:
        """Run a vector search query with the given query vector.

        Args:
            query_vector: The embedding vector to search with.
            limit: Max number of results to return.
            index_name: Name of the vector search index.
        """
        db = self.search_tester.client[self.db_name]
        collection = db[self.col_name]

        return list(
            collection.aggregate(
                [
                    {
                        "$vectorSearch": {
                            "index": index_name,
                            "path": "plot_embedding_voyage_3_large",
                            "queryVector": query_vector,
                            "numCandidates": limit * 2,
                            "limit": limit,
                        }
                    },
                    {
                        "$project": {
                            "_id": 0,
                            "title": 1,
                            "plot": 1,
                            "score": {"$meta": "vectorSearchScore"},
                        }
                    },
                ]
            )
        )
