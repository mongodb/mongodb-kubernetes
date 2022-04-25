# Mongod Init Images

This Dockerfile builds a barebones container that contains MongoDB binary and MongoDB tools

The version of MongoDB tools needed depends on the Ops Manager version.

To find out which tools version to use for Ops Manager X.Y.Z, check out the mms tag `on-prem-X.Y.Z` and navigate to the conf-hosted.properties file.

For example, for OM 4.4.14:

https://raw.githubusercontent.com/10gen/mms/on-prem-4.4.14/server/conf/conf-hosted.properties

and check the entry 

`mongotools.version=100.3.1`

```bash
docker build . -t quay.io/mongodb/mongodb-enterprise-init-mongod-rhel80:4.4.4 --build-arg distro=rhel80 --build-arg toolsVersion=100.3.1 --build-arg mongodVersion=4.4.4
docker push  quay.io/mongodb/mongodb-enterprise-init-mongod-rhel80:4.4.4
``` 

These images are used to provide Mongod binaries and tools as part of the USAF work
