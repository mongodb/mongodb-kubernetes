# MongoDB Enterprise Database

This directory hosts a Dockerfile that can be run locally for development purposes (see below) or
as part of a Kubernetes deployment, using the [MongoDB Enterprise Kubernetes Operator](../mongodb-kubernetes-operator).

### Running locally

You can use `make clean build run` to build and run the container.

For more details regarding the available options, run `make` or read the provided [Makefile](Makefile).


### Other useful commands

**See the status of all running Automation Agents:**

```bash
for img in $(docker ps -a -f 'ancestor=dev/mongodb-kubernetes-database' | tail -n +2 | awk '{print $1}'); do echo; echo "$img"; echo "---"; docker exec -t "$img" ps -ef; echo "---"; done
```

**Connect to a running container:**

```bash
docker exec -it $(docker ps -a -f 'ancestor=dev/mongodb-kubernetes-database' | tail -n +2 | awk '{print $1}') /bin/bash
```

## RHEL based Images

We have provided a second Dockerfile (`Dockerfile_rhel`) based on RHEL7 instead of the `jessie-slim` that the
normal image is based on. The purpose of this second image is to be uploaded to RedHat Container Catalog to be used
in OpenShift with the MongoDb ClusterServiceVersion. See the `openshift` directory in this repo.

This image can't be built in any host, because it will require the use of a subscription service with Redhat. A RHEL
host, with subscription service enabled, is required. That's the reason behind using the Redhat build service to build
this images with.

### Building locally

For building the MongoDB Database image locally use the example command:

```bash
VERSION="1.3.0"
BASE_REPO_URL=""
docker buildx build --load --progress plain --platform linux/amd64,linux/arm64,linux/s390x,linux/ppc64le . -f docker/mongodb-kubernetes-database/Dockerfile -t "${BASE_REPO_URL}mongodb-kubernetes-database:${VERSION}" \
 --build-arg VERSION="${VERSION}"

docker push "${BASE_REPO_URL}mongodb-kubernetes-database:${VERSION}"
```
