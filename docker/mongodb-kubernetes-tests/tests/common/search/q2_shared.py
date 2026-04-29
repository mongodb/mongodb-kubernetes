"""Shared constants and smoke-step helpers for Q2 search e2e tests.

Used by single-cluster RS/sharded scaffolds and the multi-cluster harness.
Keeps the per-test files focused on source-construction and topology-specific
assertions.
"""

from typing import Callable

from kubetester.mongodb import MongoDB
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import verify_text_search_query

# User credentials shared by Q2 SC scaffolds.
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

ENVOY_PROXY_PORT = 27028
MDBS_TLS_CERT_PREFIX = "certs"

# Type alias: callable that returns a SearchTester for given (mdb, username, password, use_ssl).
TesterFactory = Callable[[MongoDB, str, str, bool], SearchTester]


def q2_restore_sample(
    mdb: MongoDB,
    tools_pod: mongodb_tools_pod.ToolsPod,
    tester_factory: TesterFactory,
):
    """Restore the sample_mflix dataset using the admin tester."""
    search_tester = tester_factory(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


def q2_create_search_index(mdb: MongoDB, tester_factory: TesterFactory):
    """Create the movies search index and wait for it to be ready."""
    search_tester = tester_factory(mdb, USER_NAME, USER_PASSWORD, True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)


def q2_text_search_query(mdb: MongoDB, tester_factory: TesterFactory):
    """Execute the standard text search query smoke check."""
    search_tester = tester_factory(mdb, USER_NAME, USER_PASSWORD, True)
    verify_text_search_query(search_tester)
