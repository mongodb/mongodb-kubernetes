### Building locally

#### Option 1: Download from remote URL

For building the MongoDB Enterprise Ops Manager Docker image by downloading from a URL:

```bash
VERSION="8.0.7"
OM_DOWNLOAD_URL="https://downloads.mongodb.com/on-prem-mms/tar/mongodb-mms-8.0.7.500.20250505T1426Z.tar.gz"
docker buildx build --load --progress plain . -f docker/mongodb-enterprise-ops-manager/Dockerfile -t "mongodb-enterprise-ops-manager:${VERSION}" \
  --build-arg version="${VERSION}" \
  --build-arg om_download_url="${OM_DOWNLOAD_URL}"
```

#### Option 2: Use local tar.gz file

To use a locally built tar.gz file:

1. Build Ops Manager (if it fails look into [this wiki](https://wiki.corp.mongodb.com/spaces/MMS/pages/314679084/Ops+Manager+Development+-+Working+with+Build+Systems))

    ```bash
    cd ${MMS_HOME}
    bazel build --cpu=amd64 --build_env=tarball //server:package
    ```

2. Copy your local tarball to the docker directory:

    ```bash
    cp ${MMS_HOME}/bazel-bin/server/package.tar.gz docker/mongodb-enterprise-ops-manager/
    ```

3. Build using the local tarball:
    ```bash
    VERSION="local-build"
    docker buildx build --load --progress plain --platform linux/amd64 . -f docker/mongodb-enterprise-ops-manager/Dockerfile -t "mongodb-enterprise-ops-manager:${VERSION}" \
      --build-arg version="${VERSION}" \
      --build-arg use_local_tarball=true
    ```
