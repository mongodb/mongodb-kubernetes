### Building locally

For building the MongoDB Init Ops Manager image locally use the example command:

```bash
TAG="1.1.0"
VERSION="1.1.0"
docker buildx build --load --progress plain . -f docker/mongodb-kubernetes-init-ops-manager/Dockerfile -t "${TAG}" \
 --build-arg version="${VERSION}"
```
