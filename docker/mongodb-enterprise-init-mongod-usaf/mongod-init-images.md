# Mongod Init Images

This Dockerfile builds a barebones container that contains MongoDB 4.2.8 enterprise.

OM 4.4.1 requires mongodb tools 100.0.2

```bash
docker build . -t quay.io/mongodb/mongodb-enterprise-init-mongod-rhel70:4.2.8 --build-arg distro=rhel70 --build-arg toolsVersion=100.0.2
docker push quay.io/mongodb/mongodb-enterprise-init-mongod-rhel70:4.2.8

docker build . -t quay.io/mongodb/mongodb-enterprise-init-mongod-rhel80:4.2.8 --build-arg distro=rhel80 --build-arg toolsVersion=100.0.2
docker push quay.io/mongodb/mongodb-enterprise-init-mongod-rhel80:4.2.8

docker build . -t quay.io/mongodb/mongodb-enterprise-init-mongod-ubuntu1604:4.2.8 --build-arg distro=ubuntu1604 --build-arg toolsVersion=100.0.2
docker push quay.io/mongodb/mongodb-enterprise-init-mongod:ubuntu1604
``` 

These images are used to provide Mongod binaries as part of the USAF work