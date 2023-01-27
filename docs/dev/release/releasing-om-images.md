# How to release OM new images

## Publishing new OM images
1. In `evergreen.yml` change one of:

   * `ops_manager_50_latest`
   * `ops_manager_60_latest`

   to the new version you want to publish.

2. Run a patch with the relevant variant for publishing the images:
   * OM50
   ```
   evergreen patch -v publish_om50_images -t all -y -f -d "Releasing OM 5.0" -u --browse
   ```
   * OM60
   ```
   evergreen patch -v publish_om60_images -t all -y -f -d "Releasing OM 6.0" -u --browse
   ```

3. The `evergreen` patch will build, test and publish the images.

4. To run a `preflight` use the following command:

   * OM50
   ```
   evergreen patch -p ops-manager-kubernetes -v preflight_om50_images -t all -y -f -d "Pre-flight OM 5.0 images" -u --browse
   ```
   * OM60
   ```
   evergreen patch -p ops-manager-kubernetes -v preflight_om60_images -t all -y -f -d "Pre-flight OM 6.0 images" -u --browse
   ```

# Create a PR

If the evergreen patch is successful, create a PR with the following changes:

1. Change the variable `ops_manager_50_latest` or `ops_manager_44_latest`
   (whatever you are releasing) to `X.Y.Z` in `.evergreen.yml` file.

## Ops Manager 5.0 Release Only

1. Change the `ops-manager` samples to use the new version ([ops-manager.yaml](../../../deploy/crds/samples/ops-manager.yaml) and the files in [this directory](../../../public/samples/ops-manager))
1. Change the default version for the fixture `custom_version` in [conftest.py](../../../docker/mongodb-enterprise-tests/tests/conftest.py). This will allow developers to run by local tests using the same OM version.
