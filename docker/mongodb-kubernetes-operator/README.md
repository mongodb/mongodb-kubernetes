# MongoDB Enterprise Kubernetes Operator

This directory hosts the Dockerfile for the Ops Manager Operator.

### Building the source-code

```bash
CGO_ENABLED=0 GOOS=linux GOFLAGS="-mod=vendor" go build -i -o mongodb-kubernetes-operator
```

### Building the image

```bash
docker build -t mongodb-kubernetes-operator:0.1 .
```

### Running locally

```bash
docker run -e OPERATOR_ENV=local -e MONGODB_ENTERPRISE_DATABASE_IMAGE=mongodb-enterprise-database -e IMAGE_PULL_POLICY=Never mongodb-kubernetes-operator:0.1
```
