### Building locally

For building the readiness probe image locally use the example command:

```bash
VERSION="1.0.9"
TARGETARCH="amd64"
docker buildx build --load --progress plain . -f docker/mongodb-kubernetes-upgrade-hook/Dockerfile -t "mongodb-kubernetes-upgrade-hook:${VERSION}" \
 --build-arg TARGETARCH="${TARGETARCH}"
```
