### Building locally

For building the MongoDB Init AppDB image locally use the example command:

```bash
VERSION="1.3.0"
MONGODB_TOOLS_URL_UBI="https://downloads.mongodb.org/tools/db"
BASE_REPO_URL=""
docker buildx build --load --progress plain --platform linux/amd64,linux/arm64,linux/s390x,linux/ppc64le . -f docker/mongodb-kubernetes-init-database/Dockerfile -t "${BASE_REPO_URL}mongodb-kubernetes-init-database:${VERSION}" \
 --build-arg version="${VERSION}" \
 --build-arg mongodb_tools_url="${MONGODB_TOOLS_URL_UBI}" \
 --build-arg mongodb_tools_version_arm64="mongodb-database-tools-rhel93-aarch64-100.12.0.tgz" \
 --build-arg mongodb_tools_version_amd64="mongodb-database-tools-rhel93-x86_64-100.12.0.tgz" \
 --build-arg mongodb_tools_version_s390x="mongodb-database-tools-rhel9-s390x-100.12.0.tgz" \
 --build-arg mongodb_tools_version_ppc64le="mongodb-database-tools-rhel9-ppc64le-100.12.0.tgz"

docker push "${BASE_REPO_URL}mongodb-kubernetes-init-database:${VERSION}"
```
