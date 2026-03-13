#!/usr/bin/env python3
"""Standalone runner for concurrent $search cursor-paging queries.

Exercises getMore paging against a running MongoDB cluster by simulating
multiple users each iterating through search results page-by-page with a
configurable sleep between pages.

Usage:
    python run_search_paging.py <connection_string> [options]

Examples:
    # 3 users, 5 iterations, TLS with CA cert
    python run_search_paging.py \\
        "mongodb://user:pass@mongos-host:27017/?authSource=admin" \\
        --tls --ca /path/to/ca.crt \\
        --users 3 --iterations 5

    # Plain connection, unlimited iterations (Ctrl-C to stop)
    python run_search_paging.py "mongodb://user:pass@localhost:27017/" \\
        --iterations -1 --silent
"""

import argparse
import logging
import sys

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)-8s %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)

from tests.common.search.search_paging_helper import SearchPagingQueryHelper, run_concurrent_paging_queries
from tests.common.search.search_tester import SearchTester


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run concurrent $search paging queries against a MongoDB cluster.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument(
        "connection_string",
        help="Full MongoDB connection string, e.g. mongodb://user:pass@host:27017/?authSource=admin",
    )
    parser.add_argument("--tls", action="store_true", default=False, help="Enable TLS")
    parser.add_argument("--ca", metavar="PATH", default=None, help="Path to CA certificate file (implies --tls)")
    parser.add_argument("--users", type=int, default=3, help="Number of concurrent simulated users")
    parser.add_argument("--iterations", type=int, default=3, help="Full paging runs per user (-1 = unlimited)")
    parser.add_argument("--page-size", type=int, default=5, help="Documents per page (getMore batch size)")
    parser.add_argument("--page-sleep-ms", type=int, default=100, help="Sleep between page fetches in ms")
    parser.add_argument("--db", default="sample_mflix", help="Database name")
    parser.add_argument("--collection", default="movies", help="Collection name")
    parser.add_argument("--silent", action="store_true", default=False,
                        help="Suppress per-page logs; only print a summary line per completed run with QPS")
    parser.add_argument("--stop-on-error", action="store_true", default=False,
                        help="Halt all users on the first error. "
                             "Default behaviour is to log errors, continue running, and surface them in the summary.")
    return parser.parse_args()


def main():
    args = parse_args()
    use_tls = args.tls or args.ca is not None

    logging.info(
        f"Starting: {args.users} users, {args.iterations} iterations each, "
        f"page_size={args.page_size}, sleep={args.page_sleep_ms}ms, "
        f"tls={use_tls}, db={args.db}.{args.collection}"
    )

    helpers = [
        SearchPagingQueryHelper(
            search_tester=SearchTester(args.connection_string, use_ssl=use_tls, ca_path=args.ca),
            db_name=args.db,
            col_name=args.collection,
            page_size=args.page_size,
            page_sleep_ms=args.page_sleep_ms,
        )
        for _ in range(args.users)
    ]

    try:
        run_concurrent_paging_queries(
            helpers,
            iterations=args.iterations,
            silent=args.silent,
            ignore_errors=not args.stop_on_error,
        )
    except RuntimeError as e:
        logging.error(f"Run failed: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()