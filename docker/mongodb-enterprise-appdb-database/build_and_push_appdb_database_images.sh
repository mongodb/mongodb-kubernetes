#!/usr/bin/env bash
set -Eeou pipefail

_44_versions=$(jq  -rc '.supportedImages."appdb-database".versions[] | select(test("^4.4"))' < ../../release.json | sed 's/-ent//g' | tr '\n' ' ')
_50_versions=$(jq  -rc '.supportedImages."appdb-database".versions[] | select(test("^5.0"))' < ../../release.json | sed 's/-ent//g' | tr '\n' ' ')

echo "4.4 versions: ${_44_versions}"
echo "5.0 versions: ${_50_versions}"

build_id="b$(date '+%Y%m%dT000000Z')"

missing_versions=""

append_missing_version() {
    # shellcheck disable=SC2181
    if [ $? -ne 0 ]; then
        missing_versions+="${1} on ${2}"$'\n'
    fi
}

for version in $_44_versions; do
    docker build \
        -f 4.4/ubi/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent" .
    append_missing_version "${version}" "ubi"

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent"
done

for version in $_50_versions; do
    docker build \
        -f 5.0/ubi/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent" .
    append_missing_version "${version}" "ubi"

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent"
done

echo "Missing versions"
echo "${missing_versions}"
