### Building locally

For building the MongoDB Enterprise Ops Manager Docker image locally use the example command:

```bash
VERSION="8.0.7"
OM_DOWNLOAD_URL="https://downloads.mongodb.com/on-prem-mms/tar/mongodb-mms-8.0.7.500.20250505T1426Z.tar.gz"
docker buildx build --load --progress plain . -f docker/mongodb-enterprise-ops-manager/Dockerfile -t "mongodb-enterprise-ops-manager:${VERSION}" \
  --build-arg version="${VERSION}" \
  --build-arg om_download_url="${OM_DOWNLOAD_URL}"
```
