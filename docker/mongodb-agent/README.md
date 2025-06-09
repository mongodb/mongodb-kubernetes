# Mongodb-Agent
The agent gets released in a matrix style with the init-database image, which gets tagged with the operator version.
This works by using the multi-stage pattern and build-args. First - retrieve the `init-database:<version>` and retrieve the
binaries from there. Then we continue with the other steps to fully build the image.

### Building locally

For building the MongoDB Agent image locally use the example command:

```bash
VERSION="108.0.7.8810-1"
INIT_DATABASE_IMAGE="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-init-database:1.1.0"
MONGODB_TOOLS_URL_UBI="https://downloads.mongodb.org/tools/db/mongodb-database-tools-rhel93-x86_64-100.12.0.tgz"
MONGODB_AGENT_URL_UBI="https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-108.0.7.8810-1.rhel9_x86_64.tar.gz"
docker buildx build --load --progress plain . -f docker/mongodb-agent/Dockerfile -t "mongodb-agent:${VERSION}_1.1.0" \
 --build-arg version="${VERSION}" \
 --build-arg init_database_image="${INIT_DATABASE_IMAGE}" \
 --build-arg mongodb_tools_url_ubi="${MONGODB_TOOLS_URL_UBI}" \
 --build-arg mongodb_agent_url_ubi="${MONGODB_AGENT_URL_UBI}"
```
