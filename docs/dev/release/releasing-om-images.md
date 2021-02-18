# How to release OM images

When we need to release a new OM image version `X.Y.Z`, these are the steps to follow:

## Publish new version

In order to run the e2e tests our images need to be pushed to quay.
This patch will build and publish the new version of OM.

```bash
version="X.Y.Z"
evergreen patch -v publish_ops_manager_images -t all \
         --param om_version=${version} \
         -y -f -d "Releasing Ops Manager ${version}"
```

This will add version `X.Y.Z` to the list of releases to be published with daily rebuilds, so the image itself will not be present on quay.

We can either wait for the next day, or manually trigger periodic rebuilds ad explained [here](../../running-manual-periodic-builds.md), replacing `-t all` with `-t periodic_build_ops_manager`

## Create a PR
If the evergreen patch is successful, create a PR with the following changes:

1. Change the variable `ops_manager_44_latest` or `ops_manager_42_latest`
   (whatever you are releasing) to `X.Y.Z` in `.evergreen.yml` file.

### Ops Manager 4.4 Release Only

1. Change the `ops-manager` samples to use the new version ([ops-manager.yaml](../../../deploy/crds/samples/ops-manager.yaml) and the files in [this directory](../../../public/samples/ops-manager))
1. Change the default version for the fixture `custom_version` in [conftest.py](../../../docker/mongodb-enterprise-tests/tests/conftest.py). This will allow developers to run by local tests using the same OM version.

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
