### Building locally

For building the readiness probe image locally use the example command:

```bash
VERSION="1.0.22"
TARGETARCH="amd64"
docker buildx build --load --progress plain . -f docker/mongodb-kubernetes-readinessprobe/Dockerfile -t "mongodb-kubernetes-readinessprobe:${VERSION}" \
 --build-arg TARGETARCH="${TARGETARCH}"
```
