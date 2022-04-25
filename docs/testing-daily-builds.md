# Testing Daily Builds

## How images are released

Each day a new version of each image released and supported so far will be built
and pushed to the Quay registry.

Each day the periodic-build process will build a "vertical" for each image and
version supported. A "vertical" is a collection of images sharing the same
build_id. For instance, the 5th of February of 2021, the following "vertical"
was built:

* Operator
  * 1.9.0: quay.io/mongodb/mongodb-enterprise-operator:1.9.1-b20210205T000000Z
  * 1.9.1: quay.io/mongodb/mongodb-enterprise-operator:1.9.1-b20210205T000000Z
  
* Init database:
  * 1.0.2: quay.io/mongodb/mongodb-enterprise-init-database:1.0.2-b20210205T000000Z

* Database:
  * 2.0.0: quay.io/mongodb/mongodb-enterprise-database:2.0.0-b20210205T000000Z

* Init appdb:
  * 1.0.6: quay.io/mongodb/mongodb-enterprise-init-appdb:1.0.6-b20210205T000000Z
  
* Appdb:
  * 10.2.15.5958-1\_4.2.11-ent: quay.io/mongodb/mongodb-enterprise-appdb:10.2.15.5958-1\_4.2.11-ent-b20210205T000000Z
  
* Init ops manager:
  * 1.0.3: quay.io/mongodb/mongodb-enterprise-init-ops-manager:1.0.3-b20210205T000000Z


The build in this case is `-b20210205T000000Z`, and each image (and image
version) will have a tag with this build as its suffix. The images are built
daily at the same time, 00:00 UTC, and tagged as such:

    -bYYYYMMDDT000000Z
               \-----/
                  ^-----this section is always identical

## Install a specific build

To install a specific build we will use `helm` with a command like:

    helm install mongodb-enterprise-operator helm_chart \
         --values helm_chart/values.yaml \
         --set namespace=default \
         --set build=-b20210205T000000Z

In this case we are installing a version of the Operator that will use the build
`-b20210205T000000Z` for every image

## Running E2E test

The E2E test can be configured to use an existing Operator installation, instead
of installing a new one, this way, we will run the tests against the Operator's
build that we installed in the previous run, in order to do this, pass the
`USE_RUNNING_OPERATOR=true` variable to the call to `make e2e` like:

    USE_RUNNING_OPERATOR=true make e2e light=true skip=true test=<your-test>

To see which version of the Operator you can use:

    $ kubectl get deploy/mongodb-enterprise-operator -o jsonpath='{.spec.template.spec.containers[0].image}'
    quay.io/mongodb/mongodb-enterprise-operator:1.9.1-b20210205T000000Z
