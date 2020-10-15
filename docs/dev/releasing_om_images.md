# How to release OM images

When we need to release a new OM image version `X.Y.Z`, these are the steps to follow:

## Modify and execute evergreen patch
In the [evergreen file](../../.evergreen.yml) go to the task `build_and_push_ops_manager_images` and change the `VERSIONS` expansion to contain only the string `X.Y.Z`

Manually run the patch like this:
```
evergreen patch -v build_and_push_ops_manager_images -t all -y -f -d "Building OM" -u --browse
```

## Create a PR
If the evergreen patch is successful, create a PR with the following changes:

1. Make sure to **ADD** (and not replace) `X.Y.Z` to the `VERSIONS` expansion in the [evergreen file](../../.evergreen.yml)
1. Change the variable `custom_om_version` to `X.Y.Z` (note: there are two variables with that name, one is under `ops_manager_44_latest`, the other under `ops_manager_42_latest`, change the one that matches the new released image)
1. Change the `ops-manager` samples to use the new version ([ops-manager.yaml](../../deploy/crds/samples/ops-manager.yaml) and the files in [this directory](../../public/samples/ops-manager))

## Wait for tests to finish and complete the release
1. When the tests that use the new `custom_om_version` are all green, go to step 2
   * If the tests fail, delete the image from quay and investigate
1. Pull the image from ECR
   * ```docker pull 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/mongodb-enterprise-ops-manager:X.Y.Z```
1. Re-tag the image
   * ```docker tag 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/mongodb-enterprise-ops-manager:X.Y.Z scan.connect.redhat.com/ospid-b419ca35-17b4-4655-adee-a34e704a6835/mongodb-enterprise-ops-manager:X.Y.Z```
1. Login to redhat
   * Go to the evergreen project page for [ops-manager-kubernetes](https://evergreen.mongodb.com/projects##ops-manager-kubernetes), to the section `Variables` and copy the value for `rhc_om_pid`
   * Login to docker:
     * ```docker login -u unused scan.connect.redhat.com```
     * Enter the value you copied from evergreen as the password
1. Push the image to redhat
   * ```docker push scan.connect.redhat.com/ospid-b419ca35-17b4-4655-adee-a34e704a6835/mongodb-enterprise-ops-manager:X.Y.Z```
1. Wait for the container certification test to pass by checking [here](https://connect.redhat.com/project/2207181/images)
1. Publish the image
1. Merge the PR

### Notes
The ideal way would be to directly add the `X.Y.Z` to the `VERSIONS` expansion and run the patch manually.
However, this is currently not possible since the evergreen task would fail as there will be too many `kaniko` jobs and the pods will run out of resources.

Since the releasing of images will be completely changed soon, we will work around as described here for now.
