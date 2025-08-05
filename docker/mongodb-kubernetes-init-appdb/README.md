### Building locally

For building the MongoDB Init AppDB image locally use the example command:

```bash
VERSION="1.0.1"
MONGODB_TOOLS_URL_UBI="https://downloads.mongodb.org/tools/db/mongodb-database-tools-rhel93-x86_64-100.12.0.tgz"
docker buildx build --load --progress plain . -f docker/mongodb-kubernetes-init-appdb/Dockerfile -t "mongodb-kubernetes-init-appdb:${VERSION}" \
 --build-arg version="${VERSION}" \
 --build-arg mongodb_tools_url_ubi="${MONGODB_TOOLS_URL_UBI}"
```
