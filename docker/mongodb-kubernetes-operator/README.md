# MongoDB Enterprise Kubernetes Operator

This directory hosts the Dockerfile for the Ops Manager Operator.

### Building the source-code

```bash
CGO_ENABLED=0 GOOS=linux GOFLAGS="-mod=vendor" go build -i -o mongodb-kubernetes-operator
```

### Building the image

For building the MongoDB Init Ops Manager image locally use the example command:

```bash
VERSION="1.1.0"
LOG_AUTOMATION_CONFIG_DIFF="false"
USE_RACE="false"
docker buildx build --load --progress plain . -f docker/mongodb-kubernetes-operator/Dockerfile -t "mongodb-kubernetes-operator:${VERSION}" \
 --build-arg version="${VERSION}" \
 --build-arg log_automation_config_diff="${LOG_AUTOMATION_CONFIG_DIFF}" \
 --build-arg use_race="${USE_RACE}"
```

### Running locally

```bash
docker run -e OPERATOR_ENV=local -e MONGODB_ENTERPRISE_DATABASE_IMAGE=mongodb-enterprise-database -e IMAGE_PULL_POLICY=Never mongodb-kubernetes-operator:0.1
```
