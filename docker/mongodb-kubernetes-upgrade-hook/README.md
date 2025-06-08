### Building locally

For building the readiness probe image locally use the example command:

```bash
TAG="1.0.22"
TARGETARCH="amd64"
docker buildx build --load --progress plain . -f docker/mongodb-kubernetes-upgrade-hook/Dockerfile -t "${TAG}" \
 --build-arg TARGETARCH="${TARGETARCH}"
```
