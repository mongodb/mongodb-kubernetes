# How to release OM images

When we need to release a new OM image version `X.Y.Z`, these are the steps to follow:

## Build new version

To build a new version of Ops Manager we'll use a parameterized Evergreen build:

```bash
version="4.4.6"
evergreen patch -v build_ops_manager_images -t all \
         --param om_version=${version} \
         -y -f -d "Building Ops Manager ${version}"
```

## Publish new version

Currently, it is only possible to run the e2e tests on the newly built image if it is
pushed to Quay. In order to do this, we have to use another Evergreen task:

```bash
version="4.4.6"
evergreen patch -v publish_ops_manager_images -t all \
         --param om_version=${version} \
         -y -f -d "Releasing Ops Manager ${version}"
```

## Create a PR
If the evergreen patch is successful, create a PR with the following changes:

1. Change the variable `ops_manager_44_latest` or `ops_manager_42_latest`
   (whatever you are releasing) to `X.Y.Z` in `.evergreen.yml` file.

### Ops Manager 4.4 Release Only

1. Change the `ops-manager` samples to use the new version ([ops-manager.yaml](../../deploy/crds/samples/ops-manager.yaml) and the files in [this directory](../../public/samples/ops-manager))
1. Change the default version for the fixture `custom_version` in [conftest.py](../../docker/mongodb-enterprise-tests/tests/conftest.py). This will allow developers to run by local tests using the same OM version.

## Run specific patches for your version of OM

We support both 4.2 and 4.4 versions of OM, to run a full set of tests on your
new image, you can use an `evergreen` alias like:

    evergreen patch -a patch-run-om-42 -y -f -d "Running tests on OM4.2" -u

or

    evergreen patch -a patch-run-om-44 -y -f -d "Running tests on OM4.4" -u

## Publish Release to RH Connect

1. When the tests that use the new `custom_om_version` are all green, go to step 2
   * If the tests fail, delete the image from quay and investigate
1. Pull the image from ECR
   * `docker pull 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/mongodb-enterprise-ops-manager:X.Y.Z`
1. Re-tag the image
   * `docker tag 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/mongodb-enterprise-ops-manager:X.Y.Z scan.connect.redhat.com/ospid-b419ca35-17b4-4655-adee-a34e704a6835/mongodb-enterprise-ops-manager:X.Y.Z`
1. Login to redhat
   * Go to the evergreen project page for [ops-manager-kubernetes](https://evergreen.mongodb.com/projects##ops-manager-kubernetes), to the section `Variables` and copy the value for `rhc_om_pid`
   * Login to docker:
     * `docker login -u unused scan.connect.redhat.com`
     * Enter the value you copied from evergreen as the password
1. Push the image to redhat
   * `docker push scan.connect.redhat.com/ospid-b419ca35-17b4-4655-adee-a34e704a6835/mongodb-enterprise-ops-manager:X.Y.Z`
1. Wait for the container certification test to pass by checking [here](https://connect.redhat.com/project/2207181/images)
1. Publish the image
1. Merge the PR
