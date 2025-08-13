# Mongodb-Agent
The agent gets released in a matrix style with the init-database image, which gets tagged with the operator version.
This works by using the multi-stage pattern and build-args. First - retrieve the `init-database:<version>` and retrieve the
binaries from there. Then we continue with the other steps to fully build the image.

### Building locally

For building the MongoDB Agent image locally use the example command:

```bash
VERSION="evergreen"
AGENT_VERSION="108.0.7.8810-1"
TOOLS_VERSION="100.12.0"
MONGODB_TOOLS_URL="https://downloads.mongodb.org/tools/db"
MONGODB_AGENT_URL="https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
BASE_REPO_URL="268558157000.dkr.ecr.us-east-1.amazonaws.com/lucian.tosa/"
INIT_DATABASE_IMAGE="${BASE_REPO_URL}mongodb-kubernetes-init-database:${VERSION}"
MONGODB_AGENT_BASE="mongodb-mms-automation-agent"
MONGODB_DATABASE_TOOLS_BASE="mongodb-database-tools"


docker buildx build --progress plain --platform linux/amd64,linux/arm64,linux/s390x,linux/ppc64le . -f docker/mongodb-agent/Dockerfile -t "${BASE_REPO_URL}mongodb-agent:${AGENT_VERSION}_${VERSION}" \
    --build-arg version="${VERSION}" \
    --build-arg init_database_image="${INIT_DATABASE_IMAGE}" \
    --build-arg mongodb_tools_url="${MONGODB_TOOLS_URL}" \
    --build-arg mongodb_agent_url="${MONGODB_AGENT_URL}" \
    --build-arg mongodb_agent_version_s390x="${MONGODB_AGENT_BASE}-${AGENT_VERSION}.rhel7_s390x.tar.gz" \
    --build-arg mongodb_agent_version_ppc64le="${MONGODB_AGENT_BASE}-${AGENT_VERSION}.rhel8_ppc64le.tar.gz" \
    --build-arg mongodb_agent_version_amd64="${MONGODB_AGENT_BASE}-${AGENT_VERSION}.linux_x86_64.tar.gz" \
    --build-arg mongodb_agent_version_arm64="${MONGODB_AGENT_BASE}-${AGENT_VERSION}.amzn2_aarch64.tar.gz" \
    --build-arg mongodb_tools_version_arm64="${MONGODB_DATABASE_TOOLS_BASE}-rhel93-aarch64-${TOOLS_VERSION}.tgz" \
    --build-arg mongodb_tools_version_amd64="${MONGODB_DATABASE_TOOLS_BASE}-rhel93-x86_64-${TOOLS_VERSION}.tgz" \
    --build-arg mongodb_tools_version_s390x="${MONGODB_DATABASE_TOOLS_BASE}-rhel9-s390x-${TOOLS_VERSION}.tgz" \
    --build-arg mongodb_tools_version_ppc64le="${MONGODB_DATABASE_TOOLS_BASE}-rhel9-ppc64le-${TOOLS_VERSION}.tgz"

docker push "${BASE_REPO_URL}mongodb-agent:${AGENT_VERSION}_${VERSION}"

```
