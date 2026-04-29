# Q2-MC MongoDBSearch e2e tests live in this package.
#
# All MC harness fixtures (`member_cluster_clients`, `central_cluster_client`,
# `multi_cluster_operator`, `namespace`, ...) come from
# `tests/conftest.py` (the parent), which pytest already discovers.
#
# A package-local conftest is kept so collection works even when pytest is
# invoked with this directory as the rootdir, and so future search-MC-specific
# fixtures have a home to land in without touching unrelated tests.
