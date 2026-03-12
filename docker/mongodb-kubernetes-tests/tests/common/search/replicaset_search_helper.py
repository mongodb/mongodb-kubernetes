import pymongo.errors
import yaml
from kubetester.kubetester import KubernetesTester, run_periodically
from tests import test_logger
from tests.common.search.movies_search_helper import EmbeddedMoviesSearchHelper
from tests.common.search.search_tester import SearchTester

logger = test_logger.get_test_logger(__name__)


def verify_rs_mongod_parameters(namespace: str, mdb_resource_name: str, member_count: int, timeout: int = 600):
    """Verify each RS member's mongod has mongotHost and searchIndexManagementHostAndPort set.

    Polls each pod's /data/automation-mongod.conf until both parameters are present
    in setParameter, or the timeout is reached.
    """

    def check_mongod_parameters():
        parameters_are_set = True
        pod_parameters = []
        for idx in range(member_count):
            mongod_config = yaml.safe_load(
                KubernetesTester.run_command_in_pod_container(
                    f"{mdb_resource_name}-{idx}", namespace, ["cat", "/data/automation-mongod.conf"]
                )
            )
            set_parameter = mongod_config.get("setParameter", {})
            parameters_are_set = parameters_are_set and (
                "mongotHost" in set_parameter and "searchIndexManagementHostAndPort" in set_parameter
            )
            pod_parameters.append(f"pod {idx} setParameter: {set_parameter}")

        return parameters_are_set, f'Not all pods have mongot parameters set:\n{"\n".join(pod_parameters)}'

    run_periodically(check_mongod_parameters, timeout=timeout)
    logger.info("All RS members have correct mongod search parameters")


def verify_vector_search(search_tester: SearchTester):
    """Create a vector search index on embedded_movies and verify vector search returns results.

    Creates the index, waits for it to be ready, generates a query vector via
    the Voyage embedding API, and polls until vector search returns results.
    """
    emb_helper = EmbeddedMoviesSearchHelper(search_tester)
    emb_helper.create_vector_search_index()
    emb_helper.wait_for_vector_search_index()

    query_vector = emb_helper.generate_query_vector("war movies")
    total_docs = emb_helper.count_documents_with_embeddings()

    # wait_for_vector_search_index checks that the index reports ready, but mongot may
    # still be in INITIAL_SYNC. The retry loop below handles that by catching OperationFailure.
    def _verify():
        try:
            results = emb_helper.vector_search(query_vector, limit=total_docs)
            count = len(results)
            if count > 0:
                return True, f"Vector search returned {count} results"
            return False, "Vector search returned no results"
        except pymongo.errors.OperationFailure as e:
            return False, f"Vector search failed: {e}"

    run_periodically(_verify, timeout=120, sleep_time=5, msg="vector search")
    logger.info("Vector search executed successfully")
