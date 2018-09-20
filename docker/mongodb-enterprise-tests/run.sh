#!/bin/sh

set -e

printf "Ops Manager is listening at: %s\\n" "${OM_HOST}"

# Replica Sets
if [ "${TEST_STAGE}" = "base" ]; then
    echo "* Running Replica Set Creation Tests"
    pytest test_replica_set.py -m "replica_set and create" -s

    echo "* Running Replica Set Update Tests"
    pytest test_replica_set.py -m "replica_set and update"

    echo "* Running Replica Set Deletion Tests"
    pytest test_replica_set.py -m "replica_set and delete"
fi

if [ "${TEST_STAGE}" = "with_pv" ]; then
    echo "* Running Replica Set with PersistentVolumes"
    pytest test_replica_set_pv.py -m "replica_set and create" -s
    pytest test_replica_set_pv.py -m "replica_set and delete" -s
fi

if [ "${TEST_STAGE}" = "with_ent" ]; then
    echo "* Running Replica Set with Enterprise MongoDB"
    pytest test_replica_set_ent.py -m "replica_set and create"
    pytest test_replica_set_ent.py -m "replica_set and delete"
fi

# Pytest will return with a failure exitcode if any of
# the tests fails. In which case (and because `set -e`)
# this script will inmediatelly stop.
# Only if all tests run successfully the following
# will be printed to stdout, and we can capture this
# state from outside the cluster in Evergreen.
echo "ALL_TESTS_OK"
