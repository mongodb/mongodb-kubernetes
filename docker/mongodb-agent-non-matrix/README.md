### Building locally

For building the MongoDB Agent (non-static) image locally use the example command:

TODO: What to do with label quay.expires-after=48h?
```bash
AGENT_VERSION="108.0.7.8810-1"
TOOLS_VERSION="100.12.0"
MONGODB_TOOLS_URL="https://downloads.mongodb.org/tools/db"
MONGODB_AGENT_URL="https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
BASE_REPO_URL="268558157000.dkr.ecr.us-east-1.amazonaws.com/lucian.tosa/"

docker buildx build --progress plain --platform linux/amd64,linux/arm64,linux/s390x,linux/ppc64le . -f docker/mongodb-agent-non-matrix/Dockerfile -t "${BASE_REPO_URL}mongodb-agent:${AGENT_VERSION}" \
    --build-arg version="${AGENT_VERSION}" \
    --build-arg mongodb_tools_url="${MONGODB_TOOLS_URL}" \
    --build-arg mongodb_agent_url="${MONGODB_AGENT_URL}" \
    --build-arg mongodb_agent_version_s390x="mongodb-mms-automation-agent-${AGENT_VERSION}.rhel7_s390x.tar.gz" \
    --build-arg mongodb_agent_version_ppc64le="mongodb-mms-automation-agent-${AGENT_VERSION}.rhel8_ppc64le.tar.gz" \
    --build-arg mongodb_agent_version_amd64="mongodb-mms-automation-agent-${AGENT_VERSION}.linux_x86_64.tar.gz" \
    --build-arg mongodb_agent_version_arm64="mongodb-mms-automation-agent-${AGENT_VERSION}.amzn2_aarch64.tar.gz" \
    --build-arg mongodb_tools_version_arm64="mongodb-database-tools-rhel93-aarch64-${TOOLS_VERSION}.tgz" \
    --build-arg mongodb_tools_version_amd64="mongodb-database-tools-rhel93-x86_64-${TOOLS_VERSION}.tgz" \
    --build-arg mongodb_tools_version_s390x="mongodb-database-tools-rhel9-s390x-${TOOLS_VERSION}.tgz" \
    --build-arg mongodb_tools_version_ppc64le="mongodb-database-tools-rhel9-ppc64le-${TOOLS_VERSION}.tgz"

docker push "${BASE_REPO_URL}mongodb-agent:${AGENT_VERSION}"
```
