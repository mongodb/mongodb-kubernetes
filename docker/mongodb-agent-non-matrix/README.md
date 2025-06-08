### Building locally

For building the MongoDB Agent (non-static) image locally use the example command:

TODO: What to do with label quay.expires-after=48h?
```bash
TAG="108.0.7.8810-1"
AGENT_VERSION="108.0.7.8810-1"
TOOLS_VERSION="100.12.0"
AGENT_DISTRO="rhel9_x86_64"
TOOLS_DISTRO="rhel93-x86_64"
docker buildx build --load --progress plain . -f docker/mongodb-agent/Dockerfile -t "${TAG}" \
 --build-arg version="${VERSION}" \
 --build-arg agent_version="${AGENT_VERSION}" \
 --build-arg tools_version="${TOOLS_VERSION}" \
 --build-arg agent_distro="${AGENT_DISTRO}" \
 --build-arg tools_distro="${TOOLS_DISTRO}"
```
