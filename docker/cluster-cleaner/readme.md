# Cluster-Cleaner

The `cluster-cleaner` is a series of scripts in a Docker image that it is
supposed to run on the Kubernetes cluster via CronJobs.

## Installing a new version

When making changes to any of the scripts, or adding CronJobs, the image needs
to be rebuild, just increase the version number both in `Chart.yaml` and
`Makefile`. The process of publishing a new version is:

* Make changes to bash scripts and CronJob yaml files
* Run `make build && make push`

The CronJobs will be installed, and they will always point at the `latest`
version.

## Running the scripts locally

These cleaning scripts can be run locally, just make sure you are pointing at
the right Kubernetes cluster and set the required environment variables.

### Examples

* To restart Ops Manager:

    OM_NAMESPACE=operator-testing-42-current ./clean-ops-manager.sh

* To clean failed namespaces:

    DELETE_OLDER_THAN_AMOUNT=10 DELETE_OLDER_THAN_UNIT=minutes ./clean-failed-namespaces.sh

* To clean old builder Pods:

    DELETE_OLDER_THAN_AMOUNT=1 DELETE_OLDER_THAN_UNIT=days ./delete-old-builder-pods.sh
