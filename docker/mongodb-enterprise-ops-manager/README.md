### Running locally

For building the MongoDB Enterprise Ops Manager Docker image locally use the example command:

```bash
docker buildx build --load --progress plain . -f docker/mongodb-enterprise-ops-manager/Dockerfile -t 8.0.7 --build-arg version="8.0.7" --build-arg om_download_url="https://downloads.mongodb.com/on-prem-mms/tar/mongodb-mms-8.0.7.500.20250505T1426Z.tar.gz"
```
