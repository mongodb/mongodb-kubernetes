#!/usr/bin/env bash
set -Eou pipefail

_44_versions="4.4.0 4.4.4 4.4.11"
_50_versions="5.0.1 5.0.5"

build_id="b$(date '+%Y%m%dT000000Z')"

missing_versions=""

append_missing_version() {
    # shellcheck disable=SC2181
    if [ $? -ne 0 ]; then
        missing_versions+="${1} on ${2}"$'\n'
    fi
}

for version in $_40_versions; do
    echo "Building version ${version} -- Ubuntu"
    docker build \
        -f 4.0/ubuntu/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent" .
    append_missing_version "${version}" "ubuntu"

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent"

    echo "Building version ${version} -- UBI"
    docker build \
        -f 4.0/ubi/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent" .

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent"
    append_missing_version "${version}" "ubi"

done

for version in $_42_versions; do
    echo "Building version ${version} -- Ubuntu"
    docker build \
        -f 4.2/ubuntu/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent" .
    append_missing_version "${version}" "ubuntu"

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent"

    echo "Building version ${version} -- UBI"
    docker build \
        -f 4.2/ubi/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent" .
    append_missing_version "${version}" "ubi"

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:${version}-ent"

done

for version in $_44_versions; do
    echo "Building version ${version}"
    docker build \
        -f 4.4/ubuntu/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent" .
    append_missing_version "${version}" "ubuntu"

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent"

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
    echo "Building version ${version}"
    docker build \
        -f 5.0/ubuntu/Dockerfile \
        --build-arg MONGO_PACKAGE=mongodb-enterprise \
        --build-arg "MONGO_VERSION=${version}" \
        --build-arg MONGO_REPO=repo.mongodb.com \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}" \
        -t "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent" .
    append_missing_version "${version}" "ubuntu"

    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent-${build_id}"
    docker push "quay.io/mongodb/mongodb-enterprise-appdb-database:${version}-ent"

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
