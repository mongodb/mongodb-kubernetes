# How to release OM new images

## Publishing new OM4.4 images

1. In `evergreen.yml` change `ops_manager_44_latest` to the new version you want
   to publish.
2. Run:

    evergreen patch -v publish_om44_images -t all -y -f -d "Releasing OM 4.4" -u --browse

3. The `evergreen` patch will build, test and publish the images.


## Publishing new OM4.2 images

The process to publish new OM4.2 images is similar:

1. In `evergreen.yml` change `ops_manager_42_latest` to the new version you want
   to publish.
2. Run:

    evergreen patch -v publish_om42_images -t all -y -f -d "Releasing OM 4.4" -u --browse


## Create a PR
If the evergreen patch is successful, create a PR with the following changes:

1. Change the variable `ops_manager_44_latest` or `ops_manager_42_latest`
   (whatever you are releasing) to `X.Y.Z` in `.evergreen.yml` file.

### Ops Manager 4.4 Release Only

1. Change the `ops-manager` samples to use the new version ([ops-manager.yaml](../../../deploy/crds/samples/ops-manager.yaml) and the files in [this directory](../../../public/samples/ops-manager))
1. Change the default version for the fixture `custom_version` in [conftest.py](../../../docker/mongodb-enterprise-tests/tests/conftest.py). This will allow developers to run by local tests using the same OM version.
