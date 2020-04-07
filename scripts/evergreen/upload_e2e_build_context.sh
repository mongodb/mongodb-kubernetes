#!/usr/bin/env bash
#
# A script Evergreen will use to upload the e2e build context
#
# This should be executed from root of the evergreen build dir
#

set -o nounset
set -xeo pipefail

if [ "${context:-}" = "operator" ]; then
    tar -C docker/mongodb-enterprise-operator -zcvf operator-context.tar.gz .
    tar -C docker/mongodb-enterprise-database -zcvf database-context.tar.gz .
fi

if [ "${context:-}" = "tests" ]; then
    tar -C docker/mongodb-enterprise-tests -zcvf tests-context.tar.gz .
fi
